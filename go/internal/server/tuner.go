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
	"errors"
	"io"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// ErrNoAvailableTuners は Node 版 Tuner#_initTS が投げる Error("no available tuners") と同じ。
// HTTP 層はこのエラーを 503 Tuner Resource Unavailable へ変換する (api.ts responseStreamErrorHandler)。
var ErrNoAvailableTuners = errors.New("no available tuners")

// UserRequest は Node 版 common.UserRequest の移植。
// ストリーム要求 1 件分の識別子・優先度・エージェント情報を保持する。
//
// 注: 並行実装中の internal/tuner パッケージは同名の tuner.UserRequest を定義する。
// 本パッケージは現時点で internal/tuner に依存できない (兄弟ワーカーが未着地) ため、
// フィールド順まで同一のローカル型を定義し、サーバー単体でビルド・テストできるようにしている。
// tuner パッケージ着地後は、本型を tuner.UserRequest へ 1 箇所 (Init*Stream 呼び出し前) で
// 変換する薄いアダプタを足せばよい。
type UserRequest struct {
	ID             string
	Priority       int
	Agent          string
	URL            string
	DisableDecoder bool
}

// TSFilter は Node 版 TSFilter の最小インタフェース。
// 並行実装中の *tuner.TSFilter がそのまま満たす。
type TSFilter interface {
	// Close はフィルタを閉じ、下流の出力を終了する。
	Close() error
	// Drops はこれまでに破棄した TS パケット数 (デバッグ用)。
	Drops() int64
}

// TunerDevice は Node 版 TunerDevice の最小インタフェース。
// 並行実装中の *tuner.TunerDevice がそのまま満たす。
type TunerDevice interface {
	// Status は /api/tuners, /api/tuners/{index} が返す JSON 形状を返す (TunerDevice#toJSON 相当)。
	Status() apid.TunerDevice
	// KillProcess はチューナープロセスを強制終了する (DELETE /api/tuners/{index}/process)。
	KillProcess()
}

// Tuner は HTTP 層が必要とするチューナー操作 (並行実装中の internal/tuner の凍結インタフェース相当)。
//
// 並行実装中の *tuner.Tuner が満たすメソッドと同名・同シグネチャだが、引数の UserRequest だけは
// tuner.UserRequest と型が異なるため、着地時にアダプタが必要になる (tuner.go 冒頭コメント参照)。
type Tuner interface {
	// Devices は全チューナーデバイスの状態を返す。
	Devices() []apid.TunerDevice
	// Get は index 一致のデバイスを返す。無ければ nil。
	Get(index int) TunerDevice
	// DeviceCount はデバイス数を返す。
	DeviceCount() int
	// InitChannelStream はチャンネル全体の TS ストリームを開始する。
	InitChannelStream(ch *core.ChannelItem, userReq UserRequest, output io.Writer) (TSFilter, error)
	// InitServiceStream はサービス単位の TS ストリームを開始する。
	InitServiceStream(svc *core.ServiceItem, userReq UserRequest, output io.Writer) (TSFilter, error)
	// InitProgramStream は番組単位の TS ストリームを開始する。
	InitProgramStream(prog *apid.Program, userReq UserRequest, output io.Writer) (TSFilter, error)
}

// nullTuner は Tuner 未指定時のフォールバック実装。デバイスは 0 台で、ストリーム要求は
// 常に ErrNoAvailableTuners を返す (したがって全ストリーム API が 503 になる)。
type nullTuner struct{}

func (nullTuner) Devices() []apid.TunerDevice { return []apid.TunerDevice{} }
func (nullTuner) Get(int) TunerDevice         { return nil }
func (nullTuner) DeviceCount() int            { return 0 }
func (nullTuner) InitChannelStream(*core.ChannelItem, UserRequest, io.Writer) (TSFilter, error) {
	return nil, ErrNoAvailableTuners
}
func (nullTuner) InitServiceStream(*core.ServiceItem, UserRequest, io.Writer) (TSFilter, error) {
	return nil, ErrNoAvailableTuners
}
func (nullTuner) InitProgramStream(*apid.Program, UserRequest, io.Writer) (TSFilter, error) {
	return nil, ErrNoAvailableTuners
}

var _ Tuner = nullTuner{}
