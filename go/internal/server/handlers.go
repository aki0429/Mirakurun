/*
   Copyright 2016 kanreisa
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
	"net/http"
	"sort"
	"strconv"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// queryMap は URL クエリを「キー → 最初の値」に畳む (Express req.query 相当)。
func queryMap(r *http.Request) map[string]string {
	q := r.URL.Query()
	out := make(map[string]string, len(q))
	for k, vs := range q {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

// rejectWhereQuery は common.ts rejectWhere 相当。$where があれば true。
func rejectWhereQuery(q map[string]string) bool {
	_, ok := q["$where"]
	return ok
}

// atoi は壊れた値で 0 を返す (Node の数値クエリ強制変換に近い)。
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}

func parseBool(s string) bool {
	return s == "true" || s == "1"
}

// validChannelType は type が GR/BS/CS/SKY のいずれかか返す。
func validChannelType(t string) bool {
	for _, ct := range apid.ChannelTypes {
		if string(ct) == t {
			return true
		}
	}
	return false
}

// serviceBrief は Channel.services の 1 要素 ({id,serviceId,networkId,name,type})。
type serviceBrief struct {
	ID        int64  `json:"id"`
	ServiceID int    `json:"serviceId"`
	NetworkID int    `json:"networkId"`
	Name      string `json:"name"`
	Type      *int   `json:"type,omitempty"`
}

// channelJSON は ChannelItem の API 表現。
// Node 版 api/channels.ts は deepClone(channel) を返すため、name は常に含める
// (空文字でも出力)。services も常に配列として出力する。
type channelJSON struct {
	Type     apid.ChannelType `json:"type"`
	Channel  string           `json:"channel"`
	Name     string           `json:"name"`
	Services []serviceBrief   `json:"services"`
}

// toChannelJSON は ChannelItem を API 表現に変換する。
func (s *Server) toChannelJSON(item *core.ChannelItem) channelJSON {
	services := item.GetServices()
	briefs := make([]serviceBrief, 0, len(services))
	for _, svc := range services {
		briefs = append(briefs, serviceBrief{
			ID:        svc.ID(),
			ServiceID: svc.ServiceID(),
			NetworkID: svc.NetworkID(),
			Name:      svc.Name(),
			Type:      svc.Type(),
		})
	}
	return channelJSON{Type: item.Type, Channel: item.Channel, Name: item.Name, Services: briefs}
}

// matchChannelQuery は sift(req.query) 相当の等値フィルタ (type/channel/name)。
func matchChannelQuery(q map[string]string, ch channelJSON) bool {
	for k, v := range q {
		if v == "" {
			continue
		}
		switch k {
		case "type":
			if string(ch.Type) != v {
				return false
			}
		case "channel":
			if ch.Channel != v {
				return false
			}
		case "name":
			if ch.Name != v {
				return false
			}
		}
	}
	return true
}

// hasLogoData は core.ServiceItem のロゴ画像が存在するか返す (Node Service.isLogoDataExists)。
func (s *Server) hasLogoData(svc *core.ServiceItem) bool {
	logoID := svc.LogoID()
	if logoID == nil {
		return false
	}
	return core.IsLogoDataExists(s.app.Config.LogoDataDirPath, svc.NetworkID(), *logoID)
}

// exportServiceWithLogoData は serviceItem.export() に hasLogoData を足した apid.Service を返す。
func (s *Server) exportServiceWithLogoData(svc *core.ServiceItem) apid.Service {
	out := svc.Export()
	out.HasLogoData = apid.Ptr(s.hasLogoData(svc))
	return out
}

// matchServiceQuery は services.ts の sift(req.query) 相当の等値フィルタ。
// express-openapi が宣言型へ強制変換するため、数値/真偽値キーは変換して比較する。
func matchServiceQuery(q map[string]string, svc *core.ServiceItem) bool {
	for k, v := range q {
		if v == "" {
			continue
		}
		switch k {
		case "id":
			if svc.ID() != atoi64(v) {
				return false
			}
		case "serviceId":
			if svc.ServiceID() != atoi(v) {
				return false
			}
		case "networkId":
			if svc.NetworkID() != atoi(v) {
				return false
			}
		case "name":
			if svc.Name() != v {
				return false
			}
		case "type":
			t := svc.Type()
			if t == nil || *t != atoi(v) {
				return false
			}
		case "logoId":
			l := svc.LogoID()
			if l == nil || *l != atoi(v) {
				return false
			}
		case "remoteControlKeyId":
			r := svc.RemoteControlKeyID()
			if r == nil || *r != atoi(v) {
				return false
			}
		case "epgReady":
			if svc.EPGReady() != parseBool(v) {
				return false
			}
		case "epgUpdatedAt":
			if svc.EPGUpdatedAt() != atoi64(v) {
				return false
			}
		case "channel.type":
			ch := svc.Channel()
			if ch == nil || string(ch.Type) != v {
				return false
			}
		case "channel.channel":
			ch := svc.Channel()
			if ch == nil || ch.Channel != v {
				return false
			}
		}
	}
	return true
}

// sortedServiceItems は getOrder() 昇順の ServiceItem 一覧を返す (Node 版 services.ts)。
func (s *Server) sortedServiceItems() []*core.ServiceItem {
	items := append([]*core.ServiceItem(nil), s.app.Service.Items()...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].GetOrder() < items[j].GetOrder() })
	return items
}
