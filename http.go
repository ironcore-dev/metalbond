// Copyright 2022 OnMetal authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metalbond

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"text/template"
	"time"

	yaml "gopkg.in/yaml.v2"
)

type jsonRoutes struct {
	Date             string                               `json:"date"`
	VNet             map[uint32]map[Destination][]NextHop `json:"vnet"`
	MetalBondVersion string                               `json:"metalbondVersion"`
}

type jsonServer struct {
	m *MetalBond
}

func serveJsonRouteTable(m *MetalBond, listen string) {
	js := jsonServer{
		m: m,
	}

	http.HandleFunc("/", js.mainHandler)
	http.HandleFunc("/routes.json", js.jsonHandler)
	http.HandleFunc("/routes.yaml", js.yamlHandler)

	if err := http.ListenAndServe(listen, nil); err != nil {
		if m != nil {
			m.log().Errorf("Failed to list and serve: %v", err)
		}
	}
}

var METALBOND_VERSION string

func (j *jsonServer) getJsonRoutes() (jsonRoutes, error) {
	js := jsonRoutes{
		MetalBondVersion: METALBOND_VERSION,
		Date:             time.Now().Format("2006-01-02 15:04:05"),
		VNet:             make(map[uint32]map[Destination][]NextHop),
	}

	for _, vni := range j.m.routeTable.GetVNIs() {
		js.VNet[uint32(vni)] = make(map[Destination][]NextHop)
		for dst, hops := range j.m.routeTable.GetDestinationsByVNI(vni) {
			js.VNet[uint32(vni)][dst] = append(js.VNet[uint32(vni)][dst], hops...)
		}
	}

	for vi, _ := range js.VNet {
		for _, hops := range js.VNet[vi] {
			sort.Slice(hops, func(i, j int) bool {
				return hops[i].String() < hops[j].String()
			})
		}
	}
	return js, nil
}

func (j *jsonServer) mainHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("index.html").ParseFiles("html/index.html")
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}

	js, err := j.getJsonRoutes()
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}

	err = tmpl.Execute(w, js)
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}
}

func (j *jsonServer) jsonHandler(w http.ResponseWriter, r *http.Request) {
	js, err := j.getJsonRoutes()
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}

	out, err := json.MarshalIndent(js, "", "  ")
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	_, err = w.Write(out)
	if err != nil {
		fmt.Fprintf(w, "Error: %v", err)
	}
}

func (j *jsonServer) yamlHandler(w http.ResponseWriter, r *http.Request) {
	js, err := j.getJsonRoutes()
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}

	out, err := yaml.Marshal(js)
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintf(w, "Error: %v", err)
		return
	}

	w.Header().Add("Content-Type", "text/yaml")
	_, err = w.Write(out)
	if err != nil {
		fmt.Fprintf(w, "Error: %v", err)
	}
}
