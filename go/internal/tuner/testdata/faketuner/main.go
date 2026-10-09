// faketuner は tuner パッケージのテスト用チューナーコマンド。
// 実ハードウェア無しで TunerDevice / TSFilter を end-to-end で動かすために、
// 固定ファイルの内容を stdout へ流す (cat) か、指定時間そのまま留まる (hold)。
//
// 使い方:
//
//	faketuner cat <file> [holdMs]   ファイルを出力し 188 バイト境界へ 0 埋めしたあと、
//	                                既知のセンチネルパケット (PID 0x1FFE) を 1 つ出力して待機する
//	faketuner hold <ms>             指定ミリ秒だけ待機する
//
// センチネルは「入力の消費が完了した」ことをテスト側の出力ライターが検出するために使う。
package main

import (
	"os"
	"strconv"
	"time"
)

// sentinelPacket は PID 0x1FFE の既知パケット (テスト側 magic と一致)。
func sentinelPacket() []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	p[1] = 0x1F // PID 上位 5bit
	p[2] = 0xFE // PID 下位 8bit -> 0x1FFE
	p[3] = 0x10 // adaptation_field_control = 01 (payload only), continuity_counter = 0
	copy(p[4:], []byte{0xDE, 0xAD, 0xBE, 0xEF, 'F', 'A', 'K', 'E'})
	return p
}

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "cat":
		file := os.Args[2]
		holdMs := 30000
		if len(os.Args) >= 4 {
			if v, err := strconv.Atoi(os.Args[3]); err == nil {
				holdMs = v
			}
		}
		data, err := os.ReadFile(file)
		if err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.Write(data)

		// 188 バイト境界へ 0 埋めする (末尾の端数パケットが実センチネルと混ざらないようにする)
		if rem := len(data) % 188; rem != 0 {
			_, _ = os.Stdout.Write(make([]byte, 188-rem))
		}
		_, _ = os.Stdout.Write(sentinelPacket())
		// プロセスを生かしたままにする (テスト側が Close するまで respawn を起こさないため)
		time.Sleep(time.Duration(holdMs) * time.Millisecond)
	case "hold":
		ms, _ := strconv.Atoi(os.Args[2])
		time.Sleep(time.Duration(ms) * time.Millisecond)
	default:
		os.Exit(2)
	}
}
