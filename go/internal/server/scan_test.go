/*
   Copyright 2026 aki0429 (Go port)

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"testing"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

func TestGenerateScanConfigGRDefault(t *testing.T) {
	cfg := generateScanConfig(channelScanOption{Type: apid.ChannelTypeGR})
	if cfg == nil {
		t.Fatal("nil config")
	}
	if cfg.ScanMode != "Channel" || cfg.SetDisabledOnAdd {
		t.Errorf("scanMode=%q setDisabledOnAdd=%v", cfg.ScanMode, cfg.SetDisabledOnAdd)
	}
	if len(cfg.Channels) != 50 {
		t.Fatalf("channels len = %d, want 50", len(cfg.Channels))
	}
	if cfg.Channels[0] != "13" || cfg.Channels[49] != "62" {
		t.Errorf("first=%q last=%q", cfg.Channels[0], cfg.Channels[49])
	}
}

func TestGenerateScanConfigGROptions(t *testing.T) {
	start, end := 13, 15
	cfg := generateScanConfig(channelScanOption{
		Type:              apid.ChannelTypeGR,
		StartCh:           &start,
		EndCh:             &end,
		ChannelNameFormat: "{ch00}",
		ScanModeName:      "Service",
		SetDisabledOnAdd:  apid.Ptr(true),
	})
	if cfg == nil {
		t.Fatal("nil config")
	}
	if cfg.ScanMode != "Service" || !cfg.SetDisabledOnAdd {
		t.Errorf("scanMode=%q setDisabledOnAdd=%v", cfg.ScanMode, cfg.SetDisabledOnAdd)
	}
	want := []string{"13", "14", "15"}
	if len(cfg.Channels) != 3 || cfg.Channels[0] != want[0] || cfg.Channels[1] != want[1] || cfg.Channels[2] != want[2] {
		t.Errorf("channels = %v, want %v", cfg.Channels, want)
	}
}

func TestGenerateScanConfigBSDefault(t *testing.T) {
	cfg := generateScanConfig(channelScanOption{Type: apid.ChannelTypeBS})
	if cfg == nil {
		t.Fatal("nil config")
	}
	if cfg.ScanMode != "Service" || !cfg.SetDisabledOnAdd {
		t.Errorf("BS defaults: scanMode=%q setDisabledOnAdd=%v", cfg.ScanMode, cfg.SetDisabledOnAdd)
	}
	if len(cfg.Channels) != 156 || cfg.Channels[0] != "101" || cfg.Channels[155] != "256" {
		t.Errorf("channels len=%d first=%q last=%q", len(cfg.Channels), cfg.Channels[0], cfg.Channels[len(cfg.Channels)-1])
	}
}

func TestGenerateScanConfigBSSubCh(t *testing.T) {
	start, end := 1, 2
	subStart, subEnd := 0, 1
	cfg := generateScanConfig(channelScanOption{
		Type:       apid.ChannelTypeBS,
		UseSubCh:   apid.Ptr(true),
		StartCh:    &start,
		EndCh:      &end,
		StartSubCh: &subStart,
		EndSubCh:   &subEnd,
	})
	if cfg == nil {
		t.Fatal("nil config")
	}
	want := []string{"BS01_0", "BS01_1", "BS02_0", "BS02_1"}
	if len(cfg.Channels) != len(want) {
		t.Fatalf("channels = %v, want %v", cfg.Channels, want)
	}
	for i := range want {
		if cfg.Channels[i] != want[i] {
			t.Errorf("channels[%d] = %q, want %q", i, cfg.Channels[i], want[i])
		}
	}
}

func TestGenerateScanConfigCSDefault(t *testing.T) {
	cfg := generateScanConfig(channelScanOption{Type: apid.ChannelTypeCS})
	if cfg == nil {
		t.Fatal("nil config")
	}
	if len(cfg.Channels) != 23 || cfg.Channels[0] != "CS2" || cfg.Channels[22] != "CS24" {
		t.Errorf("channels len=%d first=%q last=%q", len(cfg.Channels), cfg.Channels[0], cfg.Channels[len(cfg.Channels)-1])
	}
}

func TestGenerateScanConfigUnsupported(t *testing.T) {
	if cfg := generateScanConfig(channelScanOption{Type: apid.ChannelTypeSKY}); cfg != nil {
		t.Errorf("SKY should return nil, got %v", cfg)
	}
	if cfg := generateScanConfig(channelScanOption{}); cfg != nil {
		t.Errorf("empty type should return nil, got %v", cfg)
	}
}

func TestFormatChannelName(t *testing.T) {
	cases := []struct {
		format string
		ch     int
		subch  *int
		want   string
	}{
		{"{ch}", 13, nil, "13"},
		{"{ch00}", 1, nil, "01"},
		{"BS{ch00}_{subch}", 1, apid.Ptr(3), "BS01_3"},
		{"CS{ch}", 24, nil, "CS24"},
		{"{ch000}", 5, apid.Ptr(0), "005"},
	}
	for _, c := range cases {
		if got := formatChannelName(c.format, c.ch, c.subch); got != c.want {
			t.Errorf("formatChannelName(%q,%d,%v) = %q, want %q", c.format, c.ch, c.subch, got, c.want)
		}
	}
}

func TestGenerateChannelItems(t *testing.T) {
	services := []apid.Service{
		{ServiceID: 1, Name: "ＮＨＫ総合１"},
		{ServiceID: 2, Name: "ＮＨＫ総合２"},
	}
	// Service mode: 1 サービス 1 エントリ
	items := generateChannelItems("Service", apid.ChannelTypeGR, "T13", services, false)
	if len(items) != 2 {
		t.Fatalf("service mode len = %d, want 2", len(items))
	}
	if items[0].Name != "ＮＨＫ総合１" || items[0].ServiceID == nil || *items[0].ServiceID != 1 {
		t.Errorf("items[0] = %+v", items[0])
	}

	// Channel mode: 共通接頭辞でまとめる
	items = generateChannelItems("Channel", apid.ChannelTypeGR, "T13", services, true)
	if len(items) != 1 {
		t.Fatalf("channel mode len = %d, want 1", len(items))
	}
	if items[0].Name != "ＮＨＫ総合" {
		t.Errorf("channel mode name = %q, want ＮＨＫ総合", items[0].Name)
	}
	if !items[0].IsDisabled {
		t.Errorf("IsDisabled = false, want true")
	}
	if items[0].ServiceID != nil {
		t.Errorf("channel mode must not set serviceId: %v", items[0].ServiceID)
	}
}

func TestGenerateChannelItemForServiceFallbackName(t *testing.T) {
	it := generateChannelItemForService(apid.ChannelTypeBS, "161", apid.Service{ServiceID: 5, Name: "  "}, false)
	if it.Name != "BS161:5" {
		t.Errorf("name = %q, want BS161:5", it.Name)
	}
	if it.ServiceID == nil || *it.ServiceID != 5 {
		t.Errorf("serviceId = %v", it.ServiceID)
	}
}

func TestNaturalLess(t *testing.T) {
	if !naturalLess("T2", "T13") {
		t.Errorf("T2 < T13 expected")
	}
	if !naturalLess("101", "256") {
		t.Errorf("101 < 256 expected")
	}
	if naturalLess("256", "101") {
		t.Errorf("256 < 101 should be false")
	}
}
