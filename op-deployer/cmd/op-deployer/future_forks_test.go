package main

import (
	"encoding/json"
	"testing"
)

func TestFutureForkSchedule(t *testing.T) {
	values := map[string]string{
		"BASE_DEVNET_AMSTERDAM_TIME": "484",
		"BASE_DEVNET_GLOAS_EPOCH":    "8",
	}
	forks, err := readFutureForks(func(key string) string { return values[key] }, 100, 6)
	if err != nil {
		t.Fatal(err)
	}
	genesis := map[string]any{"config": map[string]any{"blobSchedule": map[string]any{"bpo2": map[string]any{"target": 14}}}}
	doc, config, err := applyFutureForks(genesis, forks)
	if err != nil {
		t.Fatal(err)
	}
	if string(config["amsterdamTime"]) != "484" {
		t.Fatalf("Amsterdam time = %s", config["amsterdamTime"])
	}
	var exported document
	if err := json.Unmarshal(doc["config"], &exported); err != nil {
		t.Fatal(err)
	}
	if string(exported["blobSchedule"]) != string(config["blobSchedule"]) {
		t.Fatal("genesis and chain config schedules differ")
	}
}

func TestFutureForkScheduleValidation(t *testing.T) {
	for _, values := range []map[string]string{
		{"BASE_DEVNET_AMSTERDAM_TIME": "484"},
		{"BASE_DEVNET_GLOAS_EPOCH": "8"},
		{"BASE_DEVNET_AMSTERDAM_TIME": "485", "BASE_DEVNET_GLOAS_EPOCH": "8"},
		{"BASE_DEVNET_AMSTERDAM_TIME": "484", "BASE_DEVNET_GLOAS_EPOCH": "0"},
	} {
		if _, err := readFutureForks(func(key string) string { return values[key] }, 100, 6); err == nil {
			t.Fatalf("accepted invalid schedule: %#v", values)
		}
	}
}
