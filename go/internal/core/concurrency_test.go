package core

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

// TestConcurrentAccess はストアへの並行アクセスで panic / デッドロックが起きないことを確認する。
// (この環境では cgo が無く -race を使えないため、並行実行のスモークテストで代替する)
func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Server:            &apid.ConfigServer{},
		Channels:          []apid.ConfigChannelsItem{{Name: "x", Type: apid.ChannelTypeGR, Channel: "T1", ServiceID: apid.Ptr(1)}},
		ChannelsIntegrity: "i",
		ServicesDBPath:    filepath.Join(dir, "s.json"),
		ProgramsDBPath:    filepath.Join(dir, "p.json"),
		LogoDataDirPath:   filepath.Join(dir, "logo"),
	}
	app := New(AppOptions{
		Config: cfg,
		Tuner:  &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true}},
		Clock:  job.RealClock{},
		Log:    quietLogger(),
	})
	ch := app.Channel.Items()[0]

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := int64(g*1000 + i)
				app.Program.Add(mkProgram(id, 1, 1, i, 1000+int64(i), 100), false)
				app.Program.Get(id)
				name := fmt.Sprintf("p%d", i)
				app.Program.Set(id, ProgramPatch{Name: &name})
				app.Program.FindByQuery(map[string]string{"networkId": "1"})
				app.Program.FindByNetworkID(1)
				app.Program.FindByNetworkIDAndTime(1, 1050)
				app.Program.Exists(id)
				app.Program.Len()

				app.Service.Add(newServiceItem(app, ch, g+1, i+1, name, apid.Ptr(1), apid.Ptr(0), nil, false, 0))
				app.Service.FindByChannel(ch)
				app.Service.FindByNetworkID(g + 1)
				app.Service.NetworkIDs()

				app.Channel.Get(apid.ChannelTypeGR, "T1")
				app.Channel.Items()
				app.Status.AddRPC()
				app.Status.SetEPGGathering(g, i%2 == 0)
				app.Status.GatheringNetworks()
				app.GetStatus()
			}
		}(g)
	}
	wg.Wait()

	// 後始末
	app.Job.Close()
}
