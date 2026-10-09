package tuner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/aribts"
)

// oracleCase は EPG 差分オラクルの 1 フィクスチャ分の設定。
type oracleCase struct {
	name     string
	fixture  string
	channels []apid.ConfigChannelsItem
	services []apid.Service
	chType   apid.ChannelType
	chName   string
}

// TestEPGDifferentialOracle は実フィクスチャを偽チューナー経由で TSFilter に流し、
// 解析した全 EIT 番組を Node 版が書いた programs.json と突合する (受け入れゲート)。
//
// 比較項目: networkId / serviceId / eventId / startAt / duration / name / description。
// name / description は aribts.DecodeString によりバイト一致が保証されている。
func TestEPGDifferentialOracle(t *testing.T) {
	nodeDBPath := filepath.Join(fixturesDir, "db", "programs.json")
	if _, err := os.Stat(nodeDBPath); err != nil {
		t.Skipf("node programs.json not found: %v", err)
	}
	nodeDB := loadNodePrograms(t, nodeDBPath)

	// BS: フィクスチャ services.json の networkId=4 のサービス群を BS01_0 チャンネルに割り当てる。
	bsIDs := loadFixtureServiceIDs(t, filepath.Join(fixturesDir, "db", "services.json"), 4)
	bsServices := make([]apid.Service, 0, len(bsIDs))
	for i, sid := range bsIDs {
		bsServices = append(bsServices, apid.Service{
			ID:        int64(i + 1),
			ServiceID: sid,
			NetworkID: 4,
			Name:      "BS",
			Channel:   &apid.ServiceChannel{Type: apid.ChannelTypeBS, Channel: "BS01_0"},
		})
	}

	// 地上波: sample1.ts の EIT は networkId 31938 / serviceId 52240,52241,52247。
	// (Node 版 programs.json には networkId 31938 が含まれないため、この突合は not-found 側に計上される)
	sampleServices := []apid.Service{
		{ID: 1, ServiceID: 52240, NetworkID: 31938, Name: "NEWS チャンネル", Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T13"}},
		{ID: 2, ServiceID: 52241, NetworkID: 31938, Name: "NEWS チャンネル2", Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T13"}},
		{ID: 3, ServiceID: 52247, NetworkID: 31938, Name: "NEWS チャンネル3", Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T13"}},
	}

	cases := []oracleCase{
		{
			name:     "bs01_0",
			fixture:  "bs01_0.ts",
			channels: []apid.ConfigChannelsItem{{Name: "BS01_0", Type: apid.ChannelTypeBS, Channel: "BS01_0"}},
			services: bsServices,
			chType:   apid.ChannelTypeBS,
			chName:   "BS01_0",
		},
		{
			name:     "sample1",
			fixture:  "sample1.ts",
			channels: []apid.ConfigChannelsItem{{Name: "T13", Type: apid.ChannelTypeGR, Channel: "T13"}},
			services: sampleServices,
			chType:   apid.ChannelTypeGR,
			chName:   "T13",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			fixturePath := filepath.Join(fixturesDir, c.fixture)
			if _, err := os.Stat(fixturePath); err != nil {
				t.Skipf("fixture not found: %v", err)
			}
			tuners := []apid.ConfigTunersItem{{
				Name:    "fake",
				Types:   []apid.ChannelType{c.chType},
				Command: fakeCommand("cat", fixturePath, "30000"),
			}}
			app, _ := buildApp(t, c.channels, c.services, tuners)
			ch := mustChannel(t, app, c.chType, c.chName)

			tun := newTuner(app)
			out := newSentinelWriter(false)
			tf, err := tun.InitChannelStream(ch, UserRequest{ID: "epg-oracle", Priority: -1, DisableDecoder: true}, out)
			if err != nil {
				t.Fatalf("InitChannelStream: %v", err)
			}
			if !out.waitSentinel(8 * time.Minute) {
				_ = tf.Close()
				tun.Shutdown()
				t.Fatalf("timed out waiting for fixture to be fully consumed")
			}
			_ = tf.Close()
			tun.Shutdown()

			parsed := app.Program.ItemMap()

			// フィクスチャ自体が short_event (0x4D) を持つイベントの集合。
			// Node 版 programs.json はより長い放送から蓄積されたため、この 75MB スライスに
			// short_event が無いイベントでも name/description を持ちうる。その差分はパーサ不一致では
			// ないため coverageSkip として扱い、コンフリクトには数えない。
			hasShort := fixtureShortEventKeys(t, fixturePath)

			matched, notFound, conflicts, coverageSkip := 0, 0, 0, 0
			var examples []string
			netCount := map[int]int{}
			fieldMiss := map[string]int{}

			for _, p := range parsed {
				netCount[p.NetworkID]++
				key := programKey(p.NetworkID, p.ServiceID, p.EventID)
				n, ok := nodeDB[key]
				if !ok {
					notFound++
					continue
				}
				matched++
				if n.StartAt != p.StartAt {
					conflicts++
					fieldMiss["startAt"]++
					examples = addExample(t, examples, fmt.Sprintf("%s startAt node=%d go=%d", key, n.StartAt, p.StartAt))
				}
				if n.Duration != p.Duration {
					conflicts++
					fieldMiss["duration"]++
					examples = addExample(t, examples, fmt.Sprintf("%s duration node=%d go=%d", key, n.Duration, p.Duration))
				}
				sh := hasShort[[2]int{p.ServiceID, p.EventID}]
				if !strEqual(n.Name, p.Name) {
					// Node 側のみ name を持つ (フィクスチャに short_event が無い) ならカバレッジ差。
					if !sh && n.Name != nil && p.Name == nil {
						coverageSkip++
					} else {
						conflicts++
						fieldMiss["name"]++
						examples = addExample(t, examples, fmt.Sprintf("%s name node=%q go=%q", key, strVal(n.Name), strVal(p.Name)))
					}
				}
				if !strEqual(n.Description, p.Description) {
					if !sh && n.Description != nil && p.Description == nil {
						coverageSkip++
					} else {
						conflicts++
						fieldMiss["description"]++
						examples = addExample(t, examples, fmt.Sprintf("%s description node=%q go=%q", key, strVal(n.Description), strVal(p.Description)))
					}
				}
			}

			nets := make([]int, 0, len(netCount))
			for k := range netCount {
				nets = append(nets, k)
			}
			sort.Ints(nets)
			t.Logf("ORACLE fixture=%s parsed=%d matched=%d notFoundInNodeDB=%d conflicts=%d coverageSkip=%d networks=%v", c.fixture, len(parsed), matched, notFound, conflicts, coverageSkip, nets)
			if len(fieldMiss) != 0 {
				t.Logf("ORACLE fieldMiss=%v", fieldMiss)
			}
			for _, e := range examples {
				t.Logf("ORACLE CONFLICT %s", e)
			}
			if conflicts > 0 {
				t.Errorf("%s: %d conflicting field value(s) vs Node programs.json", c.name, conflicts)
			}
		})
	}
}

// addExample はコンフリクト例を最大 200 件まで蓄積する。
func addExample(t *testing.T, examples []string, e string) []string {
	t.Helper()
	if len(examples) < 200 {
		return append(examples, e)
	}
	return examples
}

// fixtureShortEventKeys はフィクスチャの EIT を aribts で走査し、
// short_event (descriptor_tag 0x4D) を持つ (serviceId, eventId) の集合を返す。
// Node 版 programs.json との「フィクスチャに無い情報」由来の差分を切り分けるために使う。
func fixtureShortEventKeys(t *testing.T, path string) map[[2]int]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	out := map[[2]int]bool{}
	asm := aribts.NewSectionAssembler()
	for i := 0; i+188 <= len(raw); i += 188 {
		if raw[i] != 0x47 {
			continue
		}
		pkt := raw[i : i+188]
		pid := int(pkt[1]&0x1F)<<8 | int(pkt[2])
		if pid != 0x12 {
			continue
		}
		_, sections := asm.Push(pkt)
		for _, sec := range sections {
			eit, derr := aribts.DecodeEIT(sec)
			if derr != nil {
				continue
			}
			for _, ev := range eit.Events {
				for _, d := range ev.Descriptors {
					if d.Tag == 0x4D {
						out[[2]int{eit.ServiceID, ev.EventID}] = true
						break
					}
				}
			}
		}
	}
	return out
}
