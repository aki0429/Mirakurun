package aribts

import (
	"bytes"
	"testing"
)

func TestDecodePMT(t *testing.T) {
	body := []byte{0x04, 0x00, 0xC1, 0x00, 0x00, 0xE1, 0xFF, 0xF0, 0x06}
	body = append(body, 0x09, 0x04, 0x00, 0x05, 0xE9, 0x01) // program_info: CA
	body = append(body, 0x02, 0xE1, 0x11, 0xF0, 0x03, 0x52, 0x01, 0x00)
	body = append(body, 0x0F, 0xE1, 0x12, 0xF0, 0x00)
	sec := makeSection(0x02, body)
	pmt, err := DecodePMT(sec)
	if err != nil {
		t.Fatal(err)
	}
	if pmt.ProgramNumber != 0x400 || pmt.PCRPID != 0x1FF || len(pmt.ProgramInfo) != 1 || len(pmt.Streams) != 2 {
		t.Fatalf("pmt: %+v", pmt)
	}
	if ca := pmt.ProgramInfo[0].Value.(*ConditionalAccess); ca.CAPID != 0x901 {
		t.Fatalf("ca: %+v", ca)
	}
	s := pmt.Streams[0]
	if s.StreamType != 2 || s.ElementaryPID != 0x111 || len(s.ESInfo) != 1 || s.ESInfo[0].Value.(*StreamIdentifier).ComponentTag != 0 {
		t.Fatalf("stream0: %+v", s)
	}
	if s := pmt.Streams[1]; s.StreamType != 0x0F || s.ElementaryPID != 0x112 || len(s.ESInfo) != 0 {
		t.Fatalf("stream1: %+v", s)
	}
	sec[5] ^= 1
	if _, err := DecodePMT(sec); err == nil {
		t.Fatal("expected CRC error")
	}
}

func TestDecodeNIT(t *testing.T) {
	body := []byte{0x7F, 0xE1, 0xC1, 0x00, 0x00, 0xF0, 0x02, 0x40, 0x00}
	ts := []byte{0x7F, 0xE1, 0x7F, 0xE1, 0xF0, 0x09, 0xFA, 0x02, 0x12, 0x39, 0xCD, 0x03, 0x03, 0x04, 'N'}
	body = append(body, 0xF0, byte(len(ts)))
	body = append(body, ts...)
	nit, err := DecodeNIT(makeSection(0x40, body))
	if err != nil {
		t.Fatal(err)
	}
	if nit.NetworkID != 0x7FE1 || len(nit.NetworkDescriptors) != 1 || len(nit.TransportStreams) != 1 {
		t.Fatalf("nit: %+v", nit)
	}
	tsl := nit.TransportStreams[0]
	if tsl.TransportStreamID != 0x7FE1 || tsl.OriginalNetworkID != 0x7FE1 || len(tsl.Descriptors) != 2 {
		t.Fatalf("ts: %+v", tsl)
	}
	if tsl.Descriptors[0].Value.(*TerrestrialDeliverySystem).AreaCode != 0x123 || tsl.Descriptors[1].Value.(*TSInformation).RemoteControlKeyID != 3 {
		t.Fatalf("descs: %+v", tsl.Descriptors)
	}
}

func TestDecodeSDT(t *testing.T) {
	body := []byte{0x7F, 0xE1, 0xC1, 0x00, 0x00, 0x7F, 0xE1, 0xFF}
	desc := []byte{0x48, 0x06, 0x01, 0x00, 0x03, 0x0E, 'N', 'H', 0xCF, 0x03, 0x02, 0xFE, 0x05}
	body = append(body, 0x04, 0x00, 0xFF, 0x80|byte(len(desc)>>8), byte(len(desc)))
	body = append(body, desc...)
	sdt, err := DecodeSDT(makeSection(0x42, body))
	if err != nil {
		t.Fatal(err)
	}
	if sdt.TransportStreamID != 0x7FE1 || sdt.OriginalNetworkID != 0x7FE1 || len(sdt.Services) != 1 {
		t.Fatalf("sdt: %+v", sdt)
	}
	sv := sdt.Services[0]
	if sv.ServiceID != 0x400 || sv.RunningStatus != 4 || sv.FreeCAMode != 0 || sv.EITScheduleFlag != 1 || sv.EITPresentFollowingFlag != 1 || len(sv.Descriptors) != 2 {
		t.Fatalf("service: %+v", sv)
	}
	if s := sv.Descriptors[0].Value.(*ServiceDescriptor); s.ServiceType != 1 || !bytes.Equal(s.ServiceNameChar, []byte{0x0E, 'N', 'H'}) {
		t.Fatalf("service desc: %+v", s)
	}
	if l := sv.Descriptors[1].Value.(*LogoTransmission); l.LogoID != 5 {
		t.Fatalf("logo: %+v", l)
	}
}
