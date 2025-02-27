// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package envoy

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"istio.io/pkg/log"
)

// Agent manages the restarts and the life cycle of a proxy binary.  Agent
// keeps track of all running proxy epochs and their configurations.  Hot
// restarts are performed by launching a new proxy process with a strictly
// incremented restart epoch. It is up to the proxy to ensure that older epochs
// gracefully shutdown and carry over all the necessary state to the latest
// epoch.  The agent does not terminate older epochs. The initial epoch is 0.
//
// The restart protocol matches Envoy semantics for restart epochs: to
// successfully launch a new Envoy process that will replace the running Envoy
// processes, the restart epoch of the new process must be exactly 1 greater
// than the highest restart epoch of the currently running Envoy processes.
// See https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/hot_restart.html
// for more information about the Envoy hot restart protocol.
//
// Agent requires two functions "run" and "cleanup". Run function is a call to
// start the proxy and must block until the proxy exits. Cleanup function is
// executed immediately after the proxy exits and must be non-blocking since it
// is executed synchronously in the main agent control loop. Both functions
// take the proxy epoch as an argument. A typical scenario would involve epoch
// 0 followed by a failed epoch 1 start. The agent then attempts to start epoch
// 1 again.
//
// Whenever the run function returns an error, the agent assumes that the proxy
// failed to start and attempts to restart the proxy several times with an
// exponential back-off. The subsequent restart attempts may reuse the epoch
// from the failed attempt. Retry budgets are allocated whenever the desired
// configuration changes.
//
// Agent executes a single control loop that receives notifications about
// scheduled configuration updates, exits from older proxy epochs, and retry
// attempt timers. The call to schedule a configuration update will block until
// the control loop is ready to accept and process the configuration update.
type Agent interface {
	// Run starts the agent control loop and awaits for a signal on the input
	// channel to exit the loop.
	Run(ctx context.Context) error

	// Restart triggers a hot restart of envoy, applying the given config to the new process
	Restart(config interface{})
}

var errAbort = errors.New("epoch aborted")

const errOutOfMemory = "signal: killed"

// NewAgent creates a new proxy agent for the proxy start-up and clean-up functions.
func NewAgent(proxy Proxy, terminationDrainDuration time.Duration) Agent {
	return &agent{
		proxy:                    proxy,
		mu:                       &sync.Mutex{},
		statusCh:                 make(chan exitStatus),
		activeEpochs:             map[int]chan error{},
		terminationDrainDuration: terminationDrainDuration,
		currentEpoch:             -1,
	}
}

// Proxy defines command interface for a proxy
type Proxy interface {
	// IsLive returns true if the server is up and running (i.e. past initialization).
	IsLive() bool

	// Run command for a config, epoch, and abort channel
	Run(interface{}, int, <-chan error) error

	// Cleanup command for an epoch
	Cleanup(int)
}

// DrainConfig is used to signal to the Proxy that it should start draining connections
type DrainConfig struct{}

type agent struct {
	// proxy commands
	proxy Proxy

	mu           *sync.Mutex
	activeEpochs map[int]chan error

	// currentEpoch represents the epoch of the most recent proxy. When a new proxy is created this should be incremented
	currentEpoch int

	// current configuration is the highest epoch configuration
	currentConfig interface{}

	// channel for proxy exit notifications
	statusCh chan exitStatus

	// time to allow for the proxy to drain before terminating all remaining proxy processes
	terminationDrainDuration time.Duration
}

type exitStatus struct {
	epoch int
	err   error
}

func (a *agent) Restart(config interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if reflect.DeepEqual(a.currentConfig, config) {
		// Same configuration - nothing to do.
		return
	}

	log.Infof("Received new config")

	// Make sure the current epoch (if there is one) is live before performing a hot restart.
	a.waitUntilLive()

	// Increment the latest running epoch
	a.currentEpoch++
	a.currentConfig = config

	// Add the new epoch to the map.
	abortCh := make(chan error, 1)
	a.activeEpochs[a.currentEpoch] = abortCh

	go a.runWait(a.currentConfig, a.currentEpoch, abortCh)
}

// waitUntilLive waits for the current epoch (if there is one) to go live.
func (a *agent) waitUntilLive() {
	// Make sure there is a currently active epoch. If not, just return.
	if len(a.activeEpochs) == 0 {
		log.Info("no previous epoch exists, starting now")
		return
	}

	log.Infof("waiting for epoch %d to go live before performing a hot restart", a.currentEpoch)

	// Timeout after 20 seconds. Envoy internally uses a 15s timer, so we set ours a bit above that.
	interval := time.NewTicker(500 * time.Millisecond)
	timer := time.NewTimer(20 * time.Second)

	defer func() {
		interval.Stop()
		timer.Stop()
	}()

	// Do an initial check on the live state to avoid any waits if possible.
	if a.proxy.IsLive() {
		// It's live!
		return
	}

	for {
		select {
		case <-timer.C:
			log.Warnf("timed out waiting for epoch %d to go live.", a.currentEpoch)
			return
		case <-interval.C:
			if a.proxy.IsLive() {
				// It's live!
				return
			}
		}
	}
}

func (a *agent) Run(ctx context.Context) error {
	log.Info("Starting proxy agent")
	for {
		select {
		case status := <-a.statusCh:
			a.mu.Lock()
			if status.err != nil {
				if status.err.Error() == errOutOfMemory {
					log.Warnf("Envoy may have been out of memory killed. Check memory usage and limits.")
				}
				log.Errorf("Epoch %d exited with error: %v", status.epoch, status.err)
			} else {
				log.Infof("Epoch %d exited normally", status.epoch)
			}

			delete(a.activeEpochs, status.epoch)

			active := len(a.activeEpochs)
			a.mu.Unlock()

			if active == 0 {
				log.Infof("No more active epochs, terminating")
				return nil
			}

			log.Infof("%d active epochs running", active)

		case <-ctx.Done():
			a.terminate()
			log.Info("Agent has successfully terminated")
			return nil
		}
	}
}

func (a *agent) terminate() {
	log.Infof("Agent draining Proxy")
	a.Restart(DrainConfig{})
	log.Infof("Graceful termination period is %v, starting...", a.terminationDrainDuration)
	time.Sleep(a.terminationDrainDuration)
	log.Infof("Graceful termination period complete, terminating remaining proxies.")
	a.abortAll()
}

// runWait runs the start-up command as a go routine and waits for it to finish
func (a *agent) runWait(config interface{}, epoch int, abortCh <-chan error) {
	log.Infof("Epoch %d starting", epoch)
	err := a.proxy.Run(config, epoch, abortCh)
	a.proxy.Cleanup(epoch)
	a.statusCh <- exitStatus{epoch: epoch, err: err}
}

// abortAll sends abort error to all proxies
func (a *agent) abortAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for epoch, abortCh := range a.activeEpochs {
		log.Warnf("Aborting epoch %d...", epoch)
		abortCh <- errAbort
	}
	log.Warnf("Aborted all epochs")
}
