// Copyright 2019 Istio Authors
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

package analyzers

import (
	"istio.io/istio/galley/pkg/config/analysis"
	"istio.io/istio/galley/pkg/config/analysis/analyzers/auth"
	"istio.io/istio/galley/pkg/config/analysis/analyzers/deprecation"
	"istio.io/istio/galley/pkg/config/analysis/analyzers/gateway"
	"istio.io/istio/galley/pkg/config/analysis/analyzers/injection"
	"istio.io/istio/galley/pkg/config/analysis/analyzers/schema"
	"istio.io/istio/galley/pkg/config/analysis/analyzers/virtualservice"
)

// All returns all analyzers
func All() []analysis.Analyzer {
	analyzers := []analysis.Analyzer{
		// Please keep this list sorted alphabetically by pkg.name for convenience
		&auth.ServiceRoleBindingAnalyzer{},
		&deprecation.FieldAnalyzer{},
		&gateway.IngressGatewayPortAnalyzer{},
		&injection.Analyzer{},
		&injection.VersionAnalyzer{},
		&virtualservice.DestinationHostAnalyzer{},
		&virtualservice.DestinationRuleAnalyzer{},
		&virtualservice.GatewayAnalyzer{},
	}

	analyzers = append(analyzers, schema.AllValidationAnalyzers()...)

	return analyzers
}

// AllCombined returns all analyzers combined as one
func AllCombined() *analysis.CombinedAnalyzer {
	return analysis.Combine("all", All()...)
}
