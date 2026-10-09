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

// Package config は Node 版 Mirakurun config.ts / system.ts の移植。
//
// 最重要の要件は ChannelsIntegrity が Node 版
// `createHash("sha256").update(JSON.stringify(yaml.load(channels))).digest("base64")`
// と完全一致すること。本番の services.json / programs.json は __integrity__ にこの値を
// 埋め込むため、Go 版サーバーが Node 版の書いた DB を読むためには必須。
package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/job"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
	"gopkg.in/yaml.v3"
)

// 既定の設定ファイルパス (Node 版 process.env の既定値と同じ)。
const (
	DefaultServerConfigPath   = "/usr/local/etc/mirakurun/server.yml"
	DefaultTunersConfigPath   = "/usr/local/etc/mirakurun/tuners.yml"
	DefaultChannelsConfigPath = "/usr/local/etc/mirakurun/channels.yml"

	// 既定値が無いときにコピーするリポジトリ同梱の設定 (Node 版は cwd 相対の "config/*.yml")。
	DefaultServerRelative   = "config/server.yml"
	DefaultTunersRelative   = "config/tuners.yml"
	DefaultChannelsRelative = "config/channels.yml"

	// 既定の allowOrigins / tsplayEndpoint。
	DefaultTsplayEndpoint = "https://mirakurun-secure-contexts-api.pages.dev/tsplay/"
)

// DefaultAllowIPv4CidrRanges は Node 版の既定 IPv4 許可レンジ。
var DefaultAllowIPv4CidrRanges = []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// DefaultAllowIPv6CidrRanges は Node 版の既定 IPv6 許可レンジ。
var DefaultAllowIPv6CidrRanges = []string{"fc00::/7"}

// DefaultAllowOrigins は Node 版の既定 CORS 許可オリジン。
var DefaultAllowOrigins = []string{"https://mirakurun-secure-contexts-api.pages.dev"}

// Loader は設定ファイルの読み書きを行う。Env を差し替えることでテストできる。
type Loader struct {
	// ServerConfigPath は server.yml のパス。空なら SERVER_CONFIG_PATH → 既定値。
	ServerConfigPath string
	// TunersConfigPath は tuners.yml のパス。空なら TUNERS_CONFIG_PATH → 既定値。
	TunersConfigPath string
	// ChannelsConfigPath は channels.yml のパス。空なら CHANNELS_CONFIG_PATH → 既定値。
	ChannelsConfigPath string
	// DefaultConfigDir は既定設定 (config/*.yml) を探すディレクトリ。空なら "config"。
	DefaultConfigDir string
	// Env は環境変数。nil なら os.Getenv を使う。
	Env map[string]string
	// Log はロガー。nil なら mlog.Default()。
	Log *mlog.Logger
}

// NewLoader は環境変数からパスを解決した Loader を返す。
func NewLoader() *Loader { return &Loader{} }

// NewLoaderWithEnv は env を固定した Loader を返す (テスト用)。
func NewLoaderWithEnv(env map[string]string) *Loader { return &Loader{Env: env} }

func (l *Loader) env(key string) string {
	if l.Env != nil {
		return l.Env[key]
	}
	return os.Getenv(key)
}

func (l *Loader) log() *mlog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return mlog.Default()
}

func (l *Loader) serverPath() string {
	if l.ServerConfigPath != "" {
		return l.ServerConfigPath
	}
	if p := l.env("SERVER_CONFIG_PATH"); p != "" {
		return p
	}
	return DefaultServerConfigPath
}

func (l *Loader) tunersPath() string {
	if l.TunersConfigPath != "" {
		return l.TunersConfigPath
	}
	if p := l.env("TUNERS_CONFIG_PATH"); p != "" {
		return p
	}
	return DefaultTunersConfigPath
}

func (l *Loader) channelsPath() string {
	if l.ChannelsConfigPath != "" {
		return l.ChannelsConfigPath
	}
	if p := l.env("CHANNELS_CONFIG_PATH"); p != "" {
		return p
	}
	return DefaultChannelsConfigPath
}

func (l *Loader) defaultDir() string {
	if l.DefaultConfigDir != "" {
		return l.DefaultConfigDir
	}
	return "config"
}

// ensureDirAndCopy は親ディレクトリを作成し、対象が無ければ既定ファイルをコピーする。
// Node 版の mkdir(missing) + copyFile("config/x.yml") 相当。
func (l *Loader) ensureDirAndCopy(path, relName string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		l.log().Info("missing directory `%s`", dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to make directory `%s`: %w", dir, err)
		}
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		src := filepath.Join(l.defaultDir(), relName)
		l.log().Info("copying default %s to `%s`", relName, path)
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			return fmt.Errorf("failed to copy default %s to `%s`: %w", relName, path, rerr)
		}
		if werr := os.WriteFile(path, data, 0o644); werr != nil {
			return fmt.Errorf("failed to copy default %s to `%s`: %w", relName, path, werr)
		}
	}
	return nil
}

// LoadServer は server.yml を読み、Node 版と同じ既定値補完・検証・Docker 環境マージを行う。
func (l *Loader) LoadServer() (*apid.ConfigServer, error) {
	path := l.serverPath()
	if err := l.ensureDirAndCopy(path, filepath.Base(DefaultServerRelative)); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg apid.ConfigServer
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("load server config `%s`: %w", path, err)
	}

	// set default
	if cfg.AllowIPv4CidrRanges == nil {
		cfg.AllowIPv4CidrRanges = append([]string(nil), DefaultAllowIPv4CidrRanges...)
	}
	if cfg.AllowIPv6CidrRanges == nil {
		cfg.AllowIPv6CidrRanges = append([]string(nil), DefaultAllowIPv6CidrRanges...)
	}
	if cfg.AllowOrigins == nil {
		cfg.AllowOrigins = append([]string(nil), DefaultAllowOrigins...)
	}
	if cfg.AllowPNA == nil {
		cfg.AllowPNA = apid.Ptr(true)
	}
	if cfg.TsplayEndpoint == "" {
		cfg.TsplayEndpoint = DefaultTsplayEndpoint
	}

	// Docker env merge
	if l.env("DOCKER") == "YES" {
		l.mergeDockerEnv(&cfg)
	}

	if cfg.Hostname == "" {
		if h, herr := os.Hostname(); herr == nil {
			cfg.Hostname = h
			l.log().Info("detected hostname: %s", cfg.Hostname)
		}
	}

	// validate jobMaxRunning
	if cfg.JobMaxRunning != nil && (*cfg.JobMaxRunning < 1 || *cfg.JobMaxRunning > 100) {
		l.log().Error("invalid server config property `jobMaxRunning`: %d", *cfg.JobMaxRunning)
		cfg.JobMaxRunning = nil
	}
	// validate jobMaxStandby
	if cfg.JobMaxStandby != nil && (*cfg.JobMaxStandby < 1 || *cfg.JobMaxStandby > 100) {
		l.log().Error("invalid server config property `jobMaxStandby`: %d", *cfg.JobMaxStandby)
		cfg.JobMaxStandby = nil
	}

	// validate allowIPv4CidrRanges
	cfg.AllowIPv4CidrRanges = filterValidCidr(cfg.AllowIPv4CidrRanges, true, l.log())
	// validate allowIPv6CidrRanges
	cfg.AllowIPv6CidrRanges = filterValidCidr(cfg.AllowIPv6CidrRanges, false, l.log())

	return &cfg, nil
}

// mergeDockerEnv は DOCKER=YES のときの環境変数マージ (Node 版 config.ts 114-179 行)。
func (l *Loader) mergeDockerEnv(cfg *apid.ConfigServer) {
	cfg.Path = apid.Ptr("/var/run/mirakurun.sock")
	if l.env("DOCKER_NETWORK") != "host" {
		cfg.Port = apid.Ptr(40772)
		cfg.DisableIPv6 = true
	}

	if cfg.Hostname == "" {
		if h := l.env("HOSTNAME"); len(trimSpace(h)) > 0 {
			cfg.Hostname = trimSpace(h)
		}
	}
	if v := l.env("LOG_LEVEL"); isLogLevel(v) {
		n, _ := strconv.Atoi(v)
		cfg.LogLevel = apid.Ptr(n)
	}
	if v := l.env("MAX_LOG_HISTORY"); isUintString(v) {
		n, _ := strconv.Atoi(v)
		cfg.MaxLogHistory = apid.Ptr(n)
	}
	if v := l.env("JOB_MAX_RUNNING"); isUintString(v) {
		n, _ := strconv.Atoi(v)
		cfg.JobMaxRunning = apid.Ptr(n)
	}
	if v := l.env("JOB_MAX_STANDBY"); isUintString(v) {
		n, _ := strconv.Atoi(v)
		cfg.JobMaxStandby = apid.Ptr(n)
	}
	if v := l.env("MAX_BUFFER_BYTES_BEFORE_READY"); isUintString(v) {
		n, _ := strconv.Atoi(v)
		cfg.MaxBufferBytesBeforeReady = apid.Ptr(n)
	}
	if v := l.env("EVENT_END_TIMEOUT"); isUintString(v) {
		n, _ := strconv.Atoi(v)
		cfg.EventEndTimeout = apid.Ptr(n)
	}
	if v := l.env("PROGRAM_GC_JOB_SCHEDULE"); v != "" && job.IsValidCronExpression(v) {
		cfg.ProgramGCJobSchedule = v
	}
	if v := l.env("EPG_GATHERING_JOB_SCHEDULE"); v != "" && job.IsValidCronExpression(v) {
		cfg.EPGGatheringJobSchedule = v
	}
	if v := l.env("EPG_RETRIEVAL_TIME"); isUintString(v) {
		n, _ := strconv.Atoi(v)
		cfg.EPGRetrievalTime = apid.Ptr(n)
	}
	if v := l.env("LOGO_DATA_INTERVAL"); isUintString(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.LogoDataInterval = apid.Ptr(n)
		}
	}
	if l.env("DISABLE_EIT_PARSING") == "true" {
		cfg.DisableEITParsing = true
	}
	if l.env("DISABLE_WEB_UI") == "true" {
		cfg.DisableWebUI = true
	}
	if v := l.env("ALLOW_IPV4_CIDR_RANGES"); len(trimSpace(v)) > 0 {
		cfg.AllowIPv4CidrRanges = splitCSV(v)
	}
	if v := l.env("ALLOW_IPV6_CIDR_RANGES"); len(trimSpace(v)) > 0 {
		cfg.AllowIPv6CidrRanges = splitCSV(v)
	}
	if v := l.env("ALLOW_ORIGINS"); len(trimSpace(v)) > 0 {
		cfg.AllowOrigins = splitCSV(v)
	}
	switch l.env("ALLOW_PNA") {
	case "true":
		cfg.AllowPNA = apid.Ptr(true)
	case "false":
		cfg.AllowPNA = apid.Ptr(false)
	}
	if v := l.env("TSPLAY_ENDPOINT"); len(trimSpace(v)) > 0 {
		cfg.TsplayEndpoint = trimSpace(v)
	}
}

// SaveServer は server.yml を書き出す。
func (l *Loader) SaveServer(cfg *apid.ConfigServer) error {
	return l.save(l.serverPath(), cfg)
}

// LoadTuners は tuners.yml を読み込む。
//
// 差分: Node 版は Linux で `/dev/dvb` を走査して自動検出するが、Go 移植では
// 自動検出を行わず既定ファイルのコピーに留める (Windows 開発機では検出不能なため)。
func (l *Loader) LoadTuners() ([]apid.ConfigTunersItem, error) {
	path := l.tunersPath()
	if err := l.ensureDirAndCopy(path, filepath.Base(DefaultTunersRelative)); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tuners []apid.ConfigTunersItem
	if err := yaml.Unmarshal(data, &tuners); err != nil {
		return nil, fmt.Errorf("load tuners config `%s`: %w", path, err)
	}
	return tuners, nil
}

// SaveTuners は tuners.yml を書き出す。
func (l *Loader) SaveTuners(tuners []apid.ConfigTunersItem) error {
	return l.save(l.tunersPath(), tuners)
}

// LoadChannels は channels.yml を型付きで読み込む。
func (l *Loader) LoadChannels() ([]apid.ConfigChannelsItem, error) {
	data, err := l.readChannelsRaw()
	if err != nil {
		return nil, err
	}
	var channels []apid.ConfigChannelsItem
	if err := yaml.Unmarshal(data, &channels); err != nil {
		return nil, fmt.Errorf("load channels config `%s`: %w", l.channelsPath(), err)
	}
	return channels, nil
}

// LoadChannelsWithIntegrity は channels.yml を読み、型付きスライスと
// Node 互換の integrity 文字列を返す。integrity は「生の YAML を yaml.load した値」に
// 対して計算する (Channel 構築によるフィールド追加の影響を受けない)。
func (l *Loader) LoadChannelsWithIntegrity() ([]apid.ConfigChannelsItem, string, error) {
	data, err := l.readChannelsRaw()
	if err != nil {
		return nil, "", err
	}
	var channels []apid.ConfigChannelsItem
	if err := yaml.Unmarshal(data, &channels); err != nil {
		return nil, "", fmt.Errorf("load channels config `%s`: %w", l.channelsPath(), err)
	}
	integrity, err := ChannelsIntegrity(data)
	if err != nil {
		return nil, "", err
	}
	return channels, integrity, nil
}

// SaveChannels は channels.yml を書き出す。
func (l *Loader) SaveChannels(channels []apid.ConfigChannelsItem) error {
	return l.save(l.channelsPath(), channels)
}

func (l *Loader) readChannelsRaw() ([]byte, error) {
	path := l.channelsPath()
	if err := l.ensureDirAndCopy(path, filepath.Base(DefaultChannelsRelative)); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (l *Loader) save(path string, data any) error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// ---- パッケージレベル関数 (既定 Loader を使う) ----

// LoadServer は環境変数のパスから server.yml を読み込む。
func LoadServer() (*apid.ConfigServer, error) { return NewLoader().LoadServer() }

// SaveServer は環境変数のパスへ server.yml を書き出す。
func SaveServer(cfg *apid.ConfigServer) error { return NewLoader().SaveServer(cfg) }

// LoadTuners は環境変数のパスから tuners.yml を読み込む。
func LoadTuners() ([]apid.ConfigTunersItem, error) { return NewLoader().LoadTuners() }

// SaveTuners は環境変数のパスへ tuners.yml を書き出す。
func SaveTuners(tuners []apid.ConfigTunersItem) error { return NewLoader().SaveTuners(tuners) }

// LoadChannels は環境変数のパスから channels.yml を読み込む。
func LoadChannels() ([]apid.ConfigChannelsItem, error) { return NewLoader().LoadChannels() }

// LoadChannelsWithIntegrity は環境変数のパスから channels.yml と integrity を読み込む。
func LoadChannelsWithIntegrity() ([]apid.ConfigChannelsItem, string, error) {
	return NewLoader().LoadChannelsWithIntegrity()
}

// SaveChannels は環境変数のパスへ channels.yml を書き出す。
func SaveChannels(channels []apid.ConfigChannelsItem) error {
	return NewLoader().SaveChannels(channels)
}

// EncodeIntegrity は integrity 用の base64 を (テスト用に) そのまま返す。
func EncodeIntegrity(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// filterValidCidr は有効な CIDR だけを残し、無効なものはエラーログを出して捨てる。
func filterValidCidr(ranges []string, v4 bool, log *mlog.Logger) []string {
	valid := make([]string, 0, len(ranges))
	for _, r := range ranges {
		ok := isValidIPv4CidrRange(r)
		if !v4 {
			ok = isValidIPv6CidrRange(r)
		}
		if ok {
			valid = append(valid, r)
			continue
		}
		name := "allowIPv6CidrRanges"
		if v4 {
			name = "allowIPv4CidrRanges"
		}
		log.Error("invalid server config property `%s`: %s", name, r)
	}
	return valid
}

// ---- 小さなユーティリティ ----

func isUintString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isLogLevel(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
	}
	if len(s) != 1 {
		return false
	}
	return s[0] >= '0' && s[0] <= '3'
}

func splitCSV(s string) []string {
	parts := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return parts
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && isSpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// System は Node 版 system.ts の移植 (IP 許可判定と listen アドレス探索)。
type System struct {
	cfg *apid.ConfigServer
}

// NewSystem は cfg に紐づく System を作る。
func NewSystem(cfg *apid.ConfigServer) *System { return &System{cfg: cfg} }

// IsPermittedIPAddress は addr が許可 CIDR レンジに含まれるか判定する。
func (s *System) IsPermittedIPAddress(addr string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	return isPermittedIPAddress(addr, s.cfg.AllowIPv4CidrRanges, s.cfg.AllowIPv6CidrRanges)
}

// IsPermittedHost は URL のホストが localhost / allowedHostname / 許可 IP のいずれかか判定する。
func (s *System) IsPermittedHost(rawurl string, allowedHostname string) bool {
	u, err := url.Parse(rawurl)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || (allowedHostname != "" && host == allowedHostname) {
		return true
	}
	return s.IsPermittedIPAddress(host)
}

// GetIPv4AddressesForListen は許可レンジに含まれる非ループバック IPv4 アドレスを返す。
func (s *System) GetIPv4AddressesForListen() []string {
	out := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			if ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			if s.IsPermittedIPAddress(ip.String()) {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

// GetIPv6AddressesForListen は許可レンジに含まれる非ループバック IPv6 アドレスを
// `<addr>%<interface名>` 形式で返す (Node 版と同じ)。
func (s *System) GetIPv6AddressesForListen() []string {
	out := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			if ip.IsLoopback() || ip.To4() != nil {
				continue
			}
			if s.IsPermittedIPAddress(ip.String()) {
				out = append(out, ip.String()+"%"+iface.Name)
			}
		}
	}
	return out
}

// isPermittedIPAddress は Node 版 isPermittedIPAddress の移植。
func isPermittedIPAddress(addr string, ranges4, ranges6 []string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	if a.Is4() {
		for _, r := range ranges4 {
			if p, err := netip.ParsePrefix(r); err == nil && p.Contains(a) {
				return true
			}
		}
		return false
	}
	if a.Is6() {
		for _, r := range ranges6 {
			if p, err := netip.ParsePrefix(r); err == nil && p.Contains(a) {
				return true
			}
		}
	}
	return false
}

// isValidIPv4CidrRange は Node 版 ip-num Validator.isValidIPv4CidrRange 相当。
func isValidIPv4CidrRange(s string) bool {
	addr, prefix, ok := cutSlash(s)
	if !ok {
		return false
	}
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return false
	}
	n, err := strconv.Atoi(prefix)
	if err != nil || n < 0 || n > 32 {
		return false
	}
	return prefixIsDigits(prefix)
}

// isValidIPv6CidrRange は Node 版 ip-num Validator.isValidIPv6CidrRange 相当。
func isValidIPv6CidrRange(s string) bool {
	addr, prefix, ok := cutSlash(s)
	if !ok {
		return false
	}
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is6() || a.Is4() {
		return false
	}
	n, err := strconv.Atoi(prefix)
	if err != nil || n < 0 || n > 128 {
		return false
	}
	return prefixIsDigits(prefix)
}

func cutSlash(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:], i > 0 && i < len(s)-1
		}
	}
	return "", "", false
}

func prefixIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
