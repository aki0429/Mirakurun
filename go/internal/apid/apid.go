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

// Package apid は api.d.ts の JSON 形状を Go で表現する。
// Node.js 版と JSON 互換にするため、フィールド順・omitempty の有無は
// JavaScript の JSON.stringify (undefined は省略、null は出力) に合わせている。
package apid

import "encoding/json"

// ChannelType はチャンネル種別 (GR / BS / CS / SKY)。
type ChannelType string

const (
	ChannelTypeGR  ChannelType = "GR"
	ChannelTypeBS  ChannelType = "BS"
	ChannelTypeCS  ChannelType = "CS"
	ChannelTypeSKY ChannelType = "SKY"
)

// ChannelTypes は Node 版 common.channelTypes と同じ並び。
var ChannelTypes = []ChannelType{ChannelTypeGR, ChannelTypeBS, ChannelTypeCS, ChannelTypeSKY}

// Error は API エラーレスポンス。
type Error struct {
	Code   int              `json:"code"`
	Reason *string          `json:"reason"`
	Errors []ErrorOfOpenAPI `json:"errors"`
}

// ErrorOfOpenAPI はバリデーションエラーの詳細。
type ErrorOfOpenAPI struct {
	ErrorCode string `json:"errorCode,omitempty"`
	Message   string `json:"message,omitempty"`
	Location  string `json:"location,omitempty"`
}

// Channel は /api/channels の要素。
type Channel struct {
	Type     ChannelType `json:"type"`
	Channel  string      `json:"channel"`
	Name     string      `json:"name,omitempty"`
	Services []Service   `json:"services,omitempty"`
}

// ServiceChannel は Service.channel (type と channel のみ)。
type ServiceChannel struct {
	Type    ChannelType `json:"type"`
	Channel string      `json:"channel"`
}

// Service はサービス (db/services.json と API の両方で使う)。
type Service struct {
	ID                 int64           `json:"id"`
	ServiceID          int             `json:"serviceId"`
	NetworkID          int             `json:"networkId"`
	Name               string          `json:"name"`
	Type               *int            `json:"type,omitempty"`
	LogoID             *int            `json:"logoId,omitempty"`
	HasLogoData        *bool           `json:"hasLogoData,omitempty"`
	RemoteControlKeyID *int            `json:"remoteControlKeyId,omitempty"`
	EPGReady           *bool           `json:"epgReady,omitempty"`
	EPGUpdatedAt       *int64          `json:"epgUpdatedAt,omitempty"`
	Channel            *ServiceChannel `json:"channel,omitempty"`
	// LogoData は旧形式 (base64)。読み込み時の移行専用で出力はしない。
	LogoData string `json:"logoData,omitempty"`
}

// Program は番組情報。Node 版 db.Program の内部フラグ (_pf 等) も保持する。
type Program struct {
	ID           int64                `json:"id"`
	EventID      int                  `json:"eventId"`
	ServiceID    int                  `json:"serviceId"`
	NetworkID    int                  `json:"networkId"`
	StartAt      int64                `json:"startAt"`
	Duration     int64                `json:"duration"`
	IsFree       bool                 `json:"isFree"`
	Name         *string              `json:"name,omitempty"`
	Description  *string              `json:"description,omitempty"`
	Genres       []ProgramGenre       `json:"genres,omitempty"`
	Video        *ProgramVideo        `json:"video,omitempty"`
	Audios       []ProgramAudio       `json:"audios,omitempty"`
	Series       *ProgramSeries       `json:"series,omitempty"`
	Extended     *OrderedStringMap    `json:"extended,omitempty"`
	RelatedItems []ProgramRelatedItem `json:"relatedItems,omitempty"`

	// 内部フラグ (Node 版と同じく JSON に出力される)
	PF          *bool `json:"_pf,omitempty"`
	IsPresent   *bool `json:"_isPresent,omitempty"`
	IsFollowing *bool `json:"_isFollowing,omitempty"`
}

// ProgramGenre はジャンル。
type ProgramGenre struct {
	Lv1 int `json:"lv1"`
	Lv2 int `json:"lv2"`
	Un1 int `json:"un1"`
	Un2 int `json:"un2"`
}

// ProgramVideo は映像情報。type / resolution は不明時 null。
type ProgramVideo struct {
	Type          *string `json:"type"`
	Resolution    *string `json:"resolution"`
	StreamContent int     `json:"streamContent"`
	ComponentType int     `json:"componentType"`
}

// ProgramAudio は音声情報。samplingRate は不明時 -1。
type ProgramAudio struct {
	ComponentType int      `json:"componentType"`
	ComponentTag  int      `json:"componentTag"`
	IsMain        bool     `json:"isMain"`
	SamplingRate  int      `json:"samplingRate"`
	Langs         []string `json:"langs"`
}

// ProgramSeries はシリーズ情報。
type ProgramSeries struct {
	ID          int    `json:"id"`
	Repeat      int    `json:"repeat"`
	Pattern     int    `json:"pattern"`
	ExpiresAt   int64  `json:"expiresAt"`
	Episode     int    `json:"episode"`
	LastEpisode int    `json:"lastEpisode"`
	Name        string `json:"name"`
}

// ProgramRelatedItem はイベントグループ。
type ProgramRelatedItem struct {
	Type      string `json:"type"`
	NetworkID *int   `json:"networkId,omitempty"`
	ServiceID int    `json:"serviceId"`
	EventID   int    `json:"eventId"`
}

// TunerDevice は /api/tuners の要素。command / pid は未使用時 null。
type TunerDevice struct {
	Index       int           `json:"index"`
	Name        string        `json:"name"`
	Types       []ChannelType `json:"types"`
	Command     *string       `json:"command"`
	PID         *int          `json:"pid"`
	Users       []TunerUser   `json:"users"`
	IsAvailable bool          `json:"isAvailable"`
	IsRemote    bool          `json:"isRemote"`
	IsFree      bool          `json:"isFree"`
	IsUsing     bool          `json:"isUsing"`
	IsFault     bool          `json:"isFault"`
}

// TunerUser はチューナー利用者。
type TunerUser struct {
	ID             string         `json:"id"`
	Priority       int            `json:"priority"`
	Agent          string         `json:"agent,omitempty"`
	URL            string         `json:"url,omitempty"`
	DisableDecoder bool           `json:"disableDecoder,omitempty"`
	StreamSetting  *StreamSetting `json:"streamSetting,omitempty"`
	StreamInfo     StreamInfo     `json:"streamInfo,omitempty"`
}

// StreamSetting はストリーム設定。
type StreamSetting struct {
	Channel   *ConfigChannelsItem `json:"channel"`
	NetworkID *int                `json:"networkId,omitempty"`
	ServiceID *int                `json:"serviceId,omitempty"`
	EventID   *int                `json:"eventId,omitempty"`
	NoProvide bool                `json:"noProvide,omitempty"`
	ParseNIT  bool                `json:"parseNIT,omitempty"`
	ParseSDT  bool                `json:"parseSDT,omitempty"`
	ParseEIT  bool                `json:"parseEIT,omitempty"`
}

// StreamInfo は PID ごとのパケット数・ドロップ数。キーは PID の10進文字列。
type StreamInfo map[string]StreamInfoItem

// StreamInfoItem は PID 単位の統計。
type StreamInfoItem struct {
	Packet int64 `json:"packet"`
	Drop   int64 `json:"drop"`
}

// TunerProcess は /api/tuners/{index}/process。
type TunerProcess struct {
	PID int `json:"pid"`
}

// JobScheduleItem はジョブスケジュール。
type JobScheduleItem struct {
	Key      string         `json:"key"`
	Schedule string         `json:"schedule"`
	Job      JobScheduleJob `json:"job"`
}

// JobScheduleJob は JobScheduleItem.job。
type JobScheduleJob struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// JobItem はジョブの状態。
type JobItem struct {
	Key          string  `json:"key"`
	Name         string  `json:"name"`
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	RetryCount   int     `json:"retryCount"`
	IsRerunnable *bool   `json:"isRerunnable,omitempty"`
	RetryOnAbort *bool   `json:"retryOnAbort,omitempty"`
	RetryOnFail  *bool   `json:"retryOnFail,omitempty"`
	RetryMax     *int    `json:"retryMax,omitempty"`
	RetryDelay   *int    `json:"retryDelay,omitempty"`
	IsAborting   bool    `json:"isAborting"`
	HasAborted   *bool   `json:"hasAborted,omitempty"`
	HasSkipped   *bool   `json:"hasSkipped,omitempty"`
	HasFailed    *bool   `json:"hasFailed,omitempty"`
	Error        *string `json:"error,omitempty"`
	CreatedAt    int64   `json:"createdAt"`
	UpdatedAt    int64   `json:"updatedAt"`
	StartedAt    *int64  `json:"startedAt,omitempty"`
	FinishedAt   *int64  `json:"finishedAt,omitempty"`
	Duration     *int64  `json:"duration,omitempty"`
}

// EventResource / EventType。
type (
	EventResource string
	EventType     string
)

const (
	EventResourceProgram     EventResource = "program"
	EventResourceService     EventResource = "service"
	EventResourceTuner       EventResource = "tuner"
	EventResourceJob         EventResource = "job"
	EventResourceJobSchedule EventResource = "job_schedule"

	EventTypeCreate EventType = "create"
	EventTypeUpdate EventType = "update"
	EventTypeRemove EventType = "remove"
)

// Event は /api/events の要素。Data は発行時点で JSON 化済み (Node 版の deepClone 相当)。
type Event struct {
	Resource EventResource   `json:"resource"`
	Type     EventType       `json:"type"`
	Data     json.RawMessage `json:"data"`
	Time     int64           `json:"time"`
}

// ConfigServer は server.yml。
type ConfigServer struct {
	Path                      *string  `json:"path,omitempty" yaml:"path,omitempty"`
	Port                      *int     `json:"port,omitempty" yaml:"port,omitempty"`
	Hostname                  string   `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	DisableIPv6               bool     `json:"disableIPv6,omitempty" yaml:"disableIPv6,omitempty"`
	LogLevel                  *int     `json:"logLevel,omitempty" yaml:"logLevel,omitempty"`
	MaxLogHistory             *int     `json:"maxLogHistory,omitempty" yaml:"maxLogHistory,omitempty"`
	JobMaxRunning             *int     `json:"jobMaxRunning,omitempty" yaml:"jobMaxRunning,omitempty"`
	JobMaxStandby             *int     `json:"jobMaxStandby,omitempty" yaml:"jobMaxStandby,omitempty"`
	MaxBufferBytesBeforeReady *int     `json:"maxBufferBytesBeforeReady,omitempty" yaml:"maxBufferBytesBeforeReady,omitempty"`
	EventEndTimeout           *int     `json:"eventEndTimeout,omitempty" yaml:"eventEndTimeout,omitempty"`
	ProgramGCJobSchedule      string   `json:"programGCJobSchedule,omitempty" yaml:"programGCJobSchedule,omitempty"`
	EPGGatheringJobSchedule   string   `json:"epgGatheringJobSchedule,omitempty" yaml:"epgGatheringJobSchedule,omitempty"`
	EPGRetrievalTime          *int     `json:"epgRetrievalTime,omitempty" yaml:"epgRetrievalTime,omitempty"`
	LogoDataInterval          *int64   `json:"logoDataInterval,omitempty" yaml:"logoDataInterval,omitempty"`
	DisableEITParsing         bool     `json:"disableEITParsing,omitempty" yaml:"disableEITParsing,omitempty"`
	DisableWebUI              bool     `json:"disableWebUI,omitempty" yaml:"disableWebUI,omitempty"`
	AllowIPv4CidrRanges       []string `json:"allowIPv4CidrRanges,omitempty" yaml:"allowIPv4CidrRanges,omitempty"`
	AllowIPv6CidrRanges       []string `json:"allowIPv6CidrRanges,omitempty" yaml:"allowIPv6CidrRanges,omitempty"`
	AllowOrigins              []string `json:"allowOrigins" yaml:"allowOrigins,omitempty"`
	AllowPNA                  *bool    `json:"allowPNA" yaml:"allowPNA,omitempty"`
	TsplayEndpoint            string   `json:"tsplayEndpoint" yaml:"tsplayEndpoint,omitempty"`
}

// ConfigTunersItem は tuners.yml の要素。
type ConfigTunersItem struct {
	Name                   string        `json:"name" yaml:"name"`
	Types                  []ChannelType `json:"types" yaml:"types"`
	Command                string        `json:"command,omitempty" yaml:"command,omitempty"`
	DVBDevicePath          string        `json:"dvbDevicePath,omitempty" yaml:"dvbDevicePath,omitempty"`
	RemoteMirakurunHost    string        `json:"remoteMirakurunHost,omitempty" yaml:"remoteMirakurunHost,omitempty"`
	RemoteMirakurunPort    *int          `json:"remoteMirakurunPort,omitempty" yaml:"remoteMirakurunPort,omitempty"`
	RemoteMirakurunDecoder *bool         `json:"remoteMirakurunDecoder,omitempty" yaml:"remoteMirakurunDecoder,omitempty"`
	Decoder                string        `json:"decoder,omitempty" yaml:"decoder,omitempty"`
	IsDisabled             bool          `json:"isDisabled,omitempty" yaml:"isDisabled,omitempty"`
}

// ConfigChannelsItem は channels.yml の要素。
// CommandVars の値は string または数値 (json.Number 相当として any で保持)。
type ConfigChannelsItem struct {
	Name        string         `json:"name" yaml:"name"`
	Type        ChannelType    `json:"type" yaml:"type"`
	Channel     string         `json:"channel" yaml:"channel"`
	ServiceID   *int           `json:"serviceId,omitempty" yaml:"serviceId,omitempty"`
	TsmfRelTs   *int           `json:"tsmfRelTs,omitempty" yaml:"tsmfRelTs,omitempty"`
	CommandVars map[string]any `json:"commandVars,omitempty" yaml:"commandVars,omitempty"`
	IsDisabled  bool           `json:"isDisabled,omitempty" yaml:"isDisabled,omitempty"`

	// deprecated (読み込み互換用)
	Satelite  string `json:"satelite,omitempty" yaml:"satelite,omitempty"`
	Satellite string `json:"satellite,omitempty" yaml:"satellite,omitempty"`
	Space     *int   `json:"space,omitempty" yaml:"space,omitempty"`
	Freq      *int   `json:"freq,omitempty" yaml:"freq,omitempty"`
	Polarity  string `json:"polarity,omitempty" yaml:"polarity,omitempty"`
}

// Version は /api/version。
type Version struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
}

// MinMaxAvg は timerAccuracy の集計値。
type MinMaxAvg struct {
	Avg float64 `json:"avg"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Status は /api/status。
type Status struct {
	Time    int64  `json:"time"`
	Version string `json:"version"`
	Process struct {
		Arch        string         `json:"arch"`
		Platform    string         `json:"platform"`
		Versions    map[string]any `json:"versions"`
		Env         map[string]any `json:"env"`
		PID         int            `json:"pid"`
		MemoryUsage map[string]any `json:"memoryUsage"`
	} `json:"process"`
	EPG struct {
		GatheringNetworks []int `json:"gatheringNetworks"`
		StoredEvents      int   `json:"storedEvents"`
	} `json:"epg"`
	RPCCount    int `json:"rpcCount"`
	StreamCount struct {
		TunerDevice int `json:"tunerDevice"`
		TSFilter    int `json:"tsFilter"`
		Decoder     int `json:"decoder"`
	} `json:"streamCount"`
	ErrorCount struct {
		UncaughtException  int `json:"uncaughtException"`
		UnhandledRejection int `json:"unhandledRejection"`
		BufferOverflow     int `json:"bufferOverflow"`
		TunerDeviceRespawn int `json:"tunerDeviceRespawn"`
		DecoderRespawn     int `json:"decoderRespawn"`
	} `json:"errorCount"`
	TimerAccuracy struct {
		Last float64   `json:"last"`
		M1   MinMaxAvg `json:"m1"`
		M5   MinMaxAvg `json:"m5"`
		M15  MinMaxAvg `json:"m15"`
	} `json:"timerAccuracy"`
}

// Ptr は値のポインタを返すヘルパ。
func Ptr[T any](v T) *T { return &v }
