// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package pb

import "strings"

func ConvertCmdLineStrToEnumValue(routeType string) NextHopType {

	if strings.Contains(strings.ToLower(routeType), "std") {
		return NextHopType_STANDARD
	}

	if strings.Contains(strings.ToLower(routeType), "lb") {
		return NextHopType_LOADBALANCER_TARGET
	}

	if strings.Contains(strings.ToLower(routeType), "nat") {
		return NextHopType_NAT
	}
	return NextHopType_STANDARD
}
