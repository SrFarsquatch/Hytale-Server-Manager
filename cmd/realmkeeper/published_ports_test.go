package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestParseContainerPortsKeepsOnlyPublishedBindings(t *testing.T) {
	inspect := `{"HostConfig":{"NetworkMode":"bridge"},"NetworkSettings":{"Ports":{
		"8008/tcp":[{"HostIp":"0.0.0.0","HostPort":"8008"}],
		"5520/udp":[{"HostIp":"0.0.0.0","HostPort":"5520"},{"HostIp":"::","HostPort":"5520"}],
		"7777/udp":null}}}`
	all, ports, err := parseContainerPorts([]byte(inspect))
	if err != nil || all {
		t.Fatalf("all = %v, err = %v", all, err)
	}
	if !ports["5520/udp"] || !ports["8008/tcp"] || ports["7777/udp"] || ports["5520/tcp"] {
		t.Fatalf("ports = %v", ports)
	}

	all, _, err = parseContainerPorts([]byte(`{"HostConfig":{"NetworkMode":"host"},"NetworkSettings":{"Ports":{}}}`))
	if err != nil || !all {
		t.Fatalf("host networking: all = %v, err = %v", all, err)
	}
}

func TestServersListFlagsUnpublishedPorts(t *testing.T) {
	hy := testServer("hy", "hytale")
	mc := testServer("mc", "minecraft-java") // 5520/tcp, not published below
	dw := dragonwildsSettings("dw")          // 7777/udp, not published below
	app := newCapabilityTestApp(t, hy, mc, dw)
	list := func() map[string]ServerSummary {
		rec := httptest.NewRecorder()
		app.handleServersList(rec, httptest.NewRequest("GET", "/api/servers", nil))
		var out []ServerSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		byID := map[string]ServerSummary{}
		for _, s := range out {
			byID[s.ID] = s
		}
		return byID
	}

	for id, s := range list() {
		if s.PortPublished != nil {
			t.Errorf("%s: portPublished = %v before the container's ports are known", id, *s.PortPublished)
		}
	}

	app.ports.set(false, map[string]bool{"5520/udp": true})
	got := list()
	want := map[string]bool{"hy": true, "mc": false, "dw": false}
	for id, published := range want {
		if p := got[id].PortPublished; p == nil || *p != published {
			t.Errorf("%s: portPublished = %v, want %v", id, p, published)
		}
	}
}
