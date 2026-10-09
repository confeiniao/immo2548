package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

//go:embed web
var webFS embed.FS

// ----------------------------------------------------------------------------
// 隐蔽核心配置（硬编码 20 个固化 UUID，纯内存无盘运行）
// ----------------------------------------------------------------------------

const (
	ProxyWsPath = "/node-helper-a5g1e3"
)

var fixedUUIDs = []string{
	"d3b07384-d113-4638-b7a4-58a4369a4732",
	"7e4c2f81-9b15-4e33-8c76-2f3b9a1e5d48",
	"a5c819d4-3f27-4b92-91e8-7d4a2c5b8e91",
	"e2a7b8c1-4d39-4f65-8b1a-9c7d4e2f5a38",
	"9b4d3e2a-1c58-4a7b-8f3e-2d5a7c9b1e4f",
	"4f8a2c1e-7b5d-493a-8c2e-1d9b5f3a7e2c",
	"2c7b5a9e-4f1d-483b-9e2a-5c1d8b7a3f4e",
	"8e1d4c2b-5a7f-43b9-8c1e-3a9d5b7e2f4a",
	"1a9b5c3d-7e2f-48a1-9d4b-2f8c5a3e7b1d",
	"6f2e8a1c-3b9d-47a5-8c4e-5a1d7b2f9e3c",
	"3d7a9b1c-5e2f-48c4-9a1b-8e2d4c5a7f3b",
	"b1c4e7a2-9d3f-45a8-8b2c-4f7a1e5d9c2a",
	"5e8b2a1c-4f7d-493e-9c1a-2d4f8a3e7b5c",
	"c2f5a8b1-7d3e-41a9-8e4b-3a7c1e5d9b2f",
	"9a1c4e7b-2d5f-483a-8b3e-5c7a2e4d1f8a",
	"4b7e1a9c-5d2f-43a8-8c1b-9e3d5a7f2c4b",
	"7d2a5c8b-1e4f-493a-9b1e-4a8c2e7f3d5a",
	"2f8c1e5a-7b3d-45a9-8e2a-1c5d9b7a4e2f",
	"8b3e5a7c-2d1f-49c8-9a4b-7e1a3c5d8f2b",
	"5a9c2e4b-7f1d-43b8-8c5e-3d8a1e7b4f2c",
}

var (
	internalCorePort int
	upgrader         = websocket.Upgrader{
		CheckOrigin:     func(r *http.Request) bool { return true },
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
	}
)

func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func startInternalCore(port int) error {
	type clientConfig struct {
		ID    string `json:"id"`
		Level int    `json:"level"`
	}
	clients := make([]clientConfig, len(fixedUUIDs))
	for i, u := range fixedUUIDs {
		clients[i] = clientConfig{ID: u, Level: 0}
	}

	configObj := map[string]interface{}{
		"log": map[string]string{"loglevel": "none"},
		"inbounds": []interface{}{
			map[string]interface{}{
				"port":     port,
				"listen":   "127.0.0.1",
				"protocol": "vless",
				"settings": map[string]interface{}{
					"clients":    clients,
					"decryption": "none",
				},
				"streamSettings": map[string]interface{}{
					"network": "ws",
					"wsSettings": map[string]interface{}{
						"path": ProxyWsPath,
					},
				},
			},
		},
		"outbounds": []interface{}{
			map[string]string{"protocol": "freedom", "tag": "direct"},
		},
	}

	rawBytes, err := json.Marshal(configObj)
	if err != nil {
		return err
	}

	server, err := core.StartInstance("json", rawBytes)
	if err != nil {
		return err
	}
	_ = server
	return nil
}

func stealthProxyHandler(w http.ResponseWriter, r *http.Request) {
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	upstreamURL := fmt.Sprintf("ws://127.0.0.1:%d%s", internalCorePort, ProxyWsPath)
	dialer := websocket.Dialer{
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
	}
	upstreamConn, _, err := dialer.Dial(upstreamURL, nil)
	if err != nil {
		return
	}
	defer upstreamConn.Close()

	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = clientConn.Close()
			_ = upstreamConn.Close()
		})
	}

	go func() {
		defer closeBoth()
		for {
			mt, msg, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
			if err := upstreamConn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}()

	for {
		mt, msg, err := upstreamConn.ReadMessage()
		if err != nil {
			break
		}
		if err := clientConn.WriteMessage(mt, msg); err != nil {
			break
		}
	}
}

// ----------------------------------------------------------------------------
// 伪装层：Hermes Agent 原生数据结构与模拟状态机
// ----------------------------------------------------------------------------

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Progress  int       `json:"progress"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Calls       int    `json:"calls"`
}

type MemoryItem struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Category  string    `json:"category"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

type Metric struct {
	Time  time.Time `json:"time"`
	Tasks float64   `json:"tasks"`
	CPU   float64   `json:"cpu"`
	Mem   float64   `json:"mem"`
}

type AgentInfo struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
}

type ModelSettings struct {
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
	BaseURL  string `json:"baseURL"`
	Model    string `json:"model"`
}

type WeChatSettings struct {
	Enabled    bool   `json:"enabled"`
	Token      string `json:"token"`
	AppID      string `json:"appId"`
	AppSecret  string `json:"appSecret"`
	WebhookURL string `json:"webhookUrl"`
}

type Settings struct {
	Model  ModelSettings  `json:"model"`
	WeChat WeChatSettings `json:"wechat"`
}

type Store struct {
	mu            sync.RWMutex
	agent         AgentInfo
	tasks         []Task
	skills        []Skill
	memory        []MemoryItem
	logs          []LogEntry
	metrics       []Metric
	settings      Settings
	dataDir       string
	coreStatus    string
	coreStartedAt time.Time
	seq           int
}

func newStore() *Store {
	dataDir := "data"
	if exe, err := os.Executable(); err == nil {
		dataDir = filepath.Join(filepath.Dir(exe), "data")
	}
	s := &Store{
		agent: AgentInfo{
			Name:      "Hermes Agent",
			Version:   "3.2.1",
			Model:     "hermes-3-llama-3.1-70b",
			Status:    "online",
			StartedAt: time.Now(),
		},
		dataDir:       dataDir,
		coreStatus:    "running",
		coreStartedAt: time.Now(),
	}
	s.loadSettings()
	s.seed()
	return s
}

func (s *Store) loadSettings() {
	_ = os.MkdirAll(s.dataDir, 0755)
	b, err := os.ReadFile(filepath.Join(s.dataDir, "settings.json"))
	if err == nil {
		_ = json.Unmarshal(b, &s.settings)
	}
	if s.settings.Model.Provider == "" {
		s.settings.Model.Provider = "openai"
	}
}

func (s *Store) saveSettings() {
	_ = os.MkdirAll(s.dataDir, 0755)
	b, _ := json.MarshalIndent(s.settings, "", "  ")
	_ = os.WriteFile(filepath.Join(s.dataDir, "settings.json"), b, 0644)
}

func (s *Store) seed() {
	s.tasks = []Task{
		{ID: s.nextID("task"), Title: "抓取并总结今日 AI 论文", Type: "research", Status: "running", Progress: 42, Agent: "researcher", CreatedAt: time.Now().Add(-3 * time.Minute), UpdatedAt: time.Now()},
		{ID: s.nextID("task"), Title: "生成周报并推送", Type: "report", Status: "queued", Progress: 0, Agent: "writer", CreatedAt: time.Now().Add(-1 * time.Minute), UpdatedAt: time.Now()},
		{ID: s.nextID("task"), Title: "清理过期记忆条目", Type: "maintenance", Status: "completed", Progress: 100, Agent: "system", CreatedAt: time.Now().Add(-40 * time.Minute), UpdatedAt: time.Now().Add(-25 * time.Minute)},
		{ID: s.nextID("task"), Title: "代码仓库安全扫描", Type: "devops", Status: "running", Progress: 18, Agent: "engineer", CreatedAt: time.Now().Add(-7 * time.Minute), UpdatedAt: time.Now()},
	}
	s.skills = []Skill{
		{Name: "web_search", Description: "联网检索与事实核查", Enabled: true, Calls: 1284},
		{Name: "code_exec", Description: "沙箱内执行代码", Enabled: true, Calls: 642},
		{Name: "memory_read", Description: "读取长期记忆", Enabled: true, Calls: 980},
		{Name: "memory_write", Description: "写入长期记忆", Enabled: true, Calls: 331},
		{Name: "file_io", Description: "本地文件读写", Enabled: false, Calls: 57},
		{Name: "browser", Description: "浏览器自动化", Enabled: false, Calls: 12},
	}
	s.memory = []MemoryItem{
		{ID: s.nextID("mem"), Key: "user.name", Value: "Alice", Category: "profile", UpdatedAt: time.Now().Add(-2 * time.Hour)},
		{ID: s.nextID("mem"), Key: "pref.lang", Value: "zh-CN", Category: "profile", UpdatedAt: time.Now().Add(-2 * time.Hour)},
		{ID: s.nextID("mem"), Key: "project.goal", Value: "构建自主 AI 运维助手", Category: "context", UpdatedAt: time.Now().Add(-1 * time.Hour)},
		{ID: s.nextID("mem"), Key: "feedback.style", Value: "喜欢简洁直接、少废话", Category: "feedback", UpdatedAt: time.Now().Add(-20 * time.Minute)},
	}
	now := time.Now()
	for i := 0; i < 30; i++ {
		t := now.Add(-time.Duration(30-i) * time.Minute)
		s.metrics = append(s.metrics, Metric{Time: t, Tasks: float64(rand.Intn(18) + 4), CPU: 18 + rand.Float64()*42, Mem: 200 + rand.Float64()*300})
	}
	s.logs = []LogEntry{
		{Time: now.Add(-5 * time.Minute), Level: "info", Source: "core", Message: "Agent 核心已启动，加载 6 个技能"},
		{Time: now.Add(-4 * time.Minute), Level: "info", Source: "task", Message: "任务『抓取并总结今日 AI 论文』开始执行"},
		{Time: now.Add(-2 * time.Minute), Level: "warn", Source: "memory", Message: "记忆容量使用 78%"},
	}
}

func (s *Store) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", prefix, s.seq)
}

func (s *Store) appendLog(source, level, msg string) {
	s.logs = append(s.logs, LogEntry{Time: time.Now(), Level: level, Source: source, Message: msg})
	if len(s.logs) > 200 {
		s.logs = s.logs[len(s.logs)-200:]
	}
}

func (s *Store) run() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.tick()
		}
	}()
}

func (s *Store) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coreStatus != "running" {
		return
	}
	now := time.Now()

	for i := range s.tasks {
		if s.tasks[i].Status == "running" {
			s.tasks[i].Progress += rand.Intn(12) + 3
			if s.tasks[i].Progress >= 100 {
				s.tasks[i].Progress = 100
				s.tasks[i].Status = "completed"
				s.appendLog("task", "info", fmt.Sprintf("任务『%s』执行完成", s.tasks[i].Title))
			}
			s.tasks[i].UpdatedAt = now
		}
	}

	for i := range s.tasks {
		if s.tasks[i].Status == "queued" && rand.Intn(3) == 0 {
			s.tasks[i].Status = "running"
			s.tasks[i].UpdatedAt = now
			s.appendLog("task", "info", fmt.Sprintf("任务『%s』开始执行", s.tasks[i].Title))
			break
		}
	}

	srcs := []string{"core", "task", "memory", "skill", "tool"}
	msgs := []string{"心跳正常", "技能调用成功", "记忆写入完成", "外部工具返回 200", "调度器空闲", "自检通过", "上下文压缩已完成"}
	levels := []string{"info", "info", "info", "warn"}
	s.appendLog(srcs[rand.Intn(len(srcs))], levels[rand.Intn(len(levels))], msgs[rand.Intn(len(msgs))])

	s.metrics = append(s.metrics, Metric{
		Time:  now,
		Tasks: float64(rand.Intn(15) + 3),
		CPU:   15 + rand.Float64()*55,
		Mem:   180 + rand.Float64()*350,
	})
	if len(s.metrics) > 60 {
		s.metrics = s.metrics[len(s.metrics)-60:]
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(v)
}

func activePort() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "3000"
}

func (s *Store) overview(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active, completed, queued := 0, 0, 0
	for _, t := range s.tasks {
		switch t.Status {
		case "running":
			active++
		case "completed":
			completed++
		case "queued":
			queued++
		}
	}
	enabled := 0
	for _, sk := range s.skills {
		if sk.Enabled {
			enabled++
		}
	}
	latest := Metric{}
	if len(s.metrics) > 0 {
		latest = s.metrics[len(s.metrics)-1]
	}
	status := "online"
	if s.coreStatus != "running" {
		status = "offline"
	}
	agent := s.agent
	agent.Status = status
	writeJSON(w, map[string]interface{}{
		"agent":         agent,
		"coreStatus":    s.coreStatus,
		"tasks":         map[string]int{"total": len(s.tasks), "active": active, "completed": completed, "queued": queued},
		"skills":        map[string]int{"total": len(s.skills), "enabled": enabled},
		"memory":        len(s.memory),
		"uptimeSeconds": int(time.Since(s.agent.StartedAt).Seconds()),
		"latestMetric":  latest,
	})
}

func (s *Store) tasksList(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, s.tasks)
}

func (s *Store) createTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Title) == "" {
		body.Title = "未命名任务"
	}
	if strings.TrimSpace(body.Type) == "" {
		body.Type = "custom"
	}
	s.mu.Lock()
	t := Task{
		ID:        s.nextID("task"),
		Title:     body.Title,
		Type:      body.Type,
		Status:    "queued",
		Progress:  0,
		Agent:     "user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.tasks = append(s.tasks, t)
	s.appendLog("task", "info", fmt.Sprintf("用户创建任务『%s』", body.Title))
	s.mu.Unlock()
	writeJSON(w, t)
}

func (s *Store) stopTask(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	for i := range s.tasks {
		if s.tasks[i].ID == id && s.tasks[i].Status == "running" {
			s.tasks[i].Status = "stopped"
			s.tasks[i].UpdatedAt = time.Now()
			s.appendLog("task", "warn", fmt.Sprintf("任务『%s』已被用户停止", s.tasks[i].Title))
		}
	}
	s.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Store) skillsList(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, s.skills)
}

func (s *Store) toggleSkill(w http.ResponseWriter, _ *http.Request, name string) {
	s.mu.Lock()
	for i := range s.skills {
		if s.skills[i].Name == name {
			s.skills[i].Enabled = !s.skills[i].Enabled
			state := "禁用"
			if s.skills[i].Enabled {
				state = "启用"
			}
			s.appendLog("skill", "info", fmt.Sprintf("技能 %s 已%s", name, state))
		}
	}
	s.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Store) memoryList(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, s.memory)
}

func (s *Store) logsList(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, s.logs)
}

func (s *Store) metricsList(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, s.metrics)
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return "****"
	}
	return k[:2] + "****" + k[len(k)-2:]
}

func (s *Store) maskedSettings() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]interface{}{
		"model": map[string]interface{}{
			"provider": s.settings.Model.Provider,
			"baseURL":  s.settings.Model.BaseURL,
			"model":    s.settings.Model.Model,
			"keySet":   s.settings.Model.APIKey != "",
			"keyMask":  maskKey(s.settings.Model.APIKey),
		},
		"wechat": map[string]interface{}{
			"enabled":      s.settings.WeChat.Enabled,
			"tokenSet":     s.settings.WeChat.Token != "",
			"appId":        s.settings.WeChat.AppID,
			"appSecretSet": s.settings.WeChat.AppSecret != "",
			"webhookUrl":   s.settings.WeChat.WebhookURL,
		},
	}
}

func (s *Store) settingsGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.maskedSettings())
}

func (s *Store) settingsPost(w http.ResponseWriter, r *http.Request) {
	var inp struct {
		Model struct {
			Provider string `json:"provider"`
			APIKey   string `json:"apiKey"`
			BaseURL  string `json:"baseURL"`
			Model    string `json:"model"`
		} `json:"model"`
		WeChat struct {
			Enabled    bool   `json:"enabled"`
			Token      string `json:"token"`
			AppID      string `json:"appId"`
			AppSecret  string `json:"appSecret"`
			WebhookURL string `json:"webhookUrl"`
		} `json:"wechat"`
	}
	_ = json.NewDecoder(r.Body).Decode(&inp)

	s.mu.Lock()
	m := s.settings.Model
	if inp.Model.Provider != "" {
		m.Provider = inp.Model.Provider
	}
	if inp.Model.APIKey != "" {
		m.APIKey = inp.Model.APIKey
	}
	if inp.Model.BaseURL != "" {
		m.BaseURL = inp.Model.BaseURL
	}
	if inp.Model.Model != "" {
		m.Model = inp.Model.Model
	}
	s.settings.Model = m

	wch := s.settings.WeChat
	wch.Enabled = inp.WeChat.Enabled
	if inp.WeChat.Token != "" {
		wch.Token = inp.WeChat.Token
	}
	if inp.WeChat.AppID != "" {
		wch.AppID = inp.WeChat.AppID
	}
	if inp.WeChat.AppSecret != "" {
		wch.AppSecret = inp.WeChat.AppSecret
	}
	if inp.WeChat.WebhookURL != "" {
		wch.WebhookURL = inp.WeChat.WebhookURL
	}
	s.settings.WeChat = wch

	s.agent.Model = m.Model
	s.saveSettings()
	s.mu.Unlock()

	s.appendLog("settings", "info", "模型 / 微信接入设置已更新并持久化")
	writeJSON(w, s.maskedSettings())
}

func (s *Store) opsStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	core := s.coreStatus
	started := s.coreStartedAt
	running := 0
	queued := 0
	for _, t := range s.tasks {
		if t.Status == "running" {
			running++
		} else if t.Status == "queued" {
			queued++
		}
	}
	s.mu.RUnlock()

	uptime := 0
	if core == "running" {
		uptime = int(time.Since(started).Seconds())
	}
	writeJSON(w, map[string]interface{}{
		"coreStatus":    core,
		"version":       s.agent.Version,
		"model":         s.settings.Model.Model,
		"pid":           os.Getpid(),
		"port":          activePort(),
		"startedAt":     started,
		"uptimeSeconds": uptime,
		"runningTasks":  running,
		"queuedTasks":   queued,
	})
}

func (s *Store) opsAction(w http.ResponseWriter, _ *http.Request, action string) {
	s.mu.Lock()
	switch action {
	case "start":
		if s.coreStatus != "running" {
			s.coreStatus = "running"
			s.coreStartedAt = time.Now()
			s.appendLog("core", "info", "Hermes Agent 核心服务已启动")
		}
	case "stop":
		if s.coreStatus == "running" {
			s.coreStatus = "stopped"
			s.appendLog("core", "warn", "Hermes Agent 核心服务已停止（任务暂停）")
		}
	case "restart":
		s.coreStatus = "running"
		s.coreStartedAt = time.Now()
		s.appendLog("core", "info", "Hermes Agent 核心服务已重启")
	}
	status := s.coreStatus
	s.mu.Unlock()
	writeJSON(w, map[string]string{"status": status})
}

type CheckResult struct {
	Name   string `json:"name"`
	Ok     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func canDial(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func readMem() (total, avail uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			switch f[0] {
			case "MemTotal:":
				fmt.Sscanf(f[1], "%d", &total)
			case "MemAvailable:":
				fmt.Sscanf(f[1], "%d", &avail)
			}
		}
	}
	return total * 1024, avail * 1024
}

func diskSpace(path string) (free, total uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bavail * uint64(stat.Bsize)
	return free, total
}

func (s *Store) envCheck(w http.ResponseWriter, _ *http.Request) {
	var results []CheckResult
	if out, err := exec.Command("go", "version").Output(); err == nil {
		results = append(results, CheckResult{"Go 运行时", true, strings.TrimSpace(string(out))})
	} else {
		results = append(results, CheckResult{"Go 运行时", false, "未检测到 go：" + err.Error()})
	}

	results = append(results, CheckResult{"操作系统 / 架构", true, runtime.GOOS + " / " + runtime.GOARCH})

	if total, avail := readMem(); total > 0 {
		ok := avail*100/total > 15
		results = append(results, CheckResult{"内存可用", ok, fmt.Sprintf("可用 %.0f / %.0f MB", float64(avail)/1e6, float64(total)/1e6)})
	} else {
		results = append(results, CheckResult{"内存可用", false, "无法读取 /proc/meminfo"})
	}

	if free, total := diskSpace("/workspace"); total > 0 {
		ok := free*100/total > 10
		results = append(results, CheckResult{"磁盘空间", ok, fmt.Sprintf("剩余 %.1f / %.1f GB", float64(free)/1e9, float64(total)/1e9)})
	} else {
		results = append(results, CheckResult{"磁盘空间", false, "无法读取磁盘信息"})
	}

	port := activePort()
	if canDial("127.0.0.1:" + port) {
		results = append(results, CheckResult{"面板端口 " + port, true, "本地监听正常"})
	} else {
		results = append(results, CheckResult{"面板端口 " + port, false, "未监听到服务"})
	}

	if canDial("8.8.8.8:53") {
		results = append(results, CheckResult{"外网连通性", true, "DNS 可达 (8.8.8.8:53)"})
	} else {
		results = append(results, CheckResult{"外网连通性", false, "无法连接外网"})
	}

	if s.settings.Model.BaseURL != "" {
		u := s.settings.Model.BaseURL
		host := u
		if i := strings.Index(u, "://"); i >= 0 {
			host = u[i+3:]
		}
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}
		if canDial(net.JoinHostPort(host, "443")) || canDial(net.JoinHostPort(host, "80")) {
			results = append(results, CheckResult{"模型网关 " + host, true, "可达"})
		} else {
			results = append(results, CheckResult{"模型网关 " + host, false, "不可达，请检查 Base URL / 网络"})
		}
	}

	if s.settings.WeChat.Enabled {
		if s.settings.WeChat.AppID == "" || s.settings.WeChat.AppSecret == "" || s.settings.WeChat.Token == "" {
			results = append(results, CheckResult{"微信接入配置", false, "缺少 AppID / AppSecret / Token"})
		} else if s.settings.WeChat.WebhookURL == "" {
			results = append(results, CheckResult{"微信接入配置", false, "未填写回调地址"})
		} else {
			results = append(results, CheckResult{"微信接入配置", true, "配置完整"})
		}
	}

	results = append(results, CheckResult{"前端资源", true, "已编译进二进制 (embed)"})
	s.appendLog("ops", "info", fmt.Sprintf("环境自检完成：%d 项，异常 %d 项", len(results), countFail(results)))
	writeJSON(w, results)
}

func countFail(rs []CheckResult) int {
	n := 0
	for _, r := range rs {
		if !r.Ok {
			n++
		}
	}
	return n
}

// ----------------------------------------------------------------------------
// 统一路由与服务入口
// ----------------------------------------------------------------------------

func (s *Store) apiHandler(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/")
	switch {
	case p == "overview":
		s.overview(w, r)
	case p == "tasks" && r.Method == "GET":
		s.tasksList(w, r)
	case p == "tasks" && r.Method == "POST":
		s.createTask(w, r)
	case strings.HasPrefix(p, "tasks/") && strings.HasSuffix(p, "/stop"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "tasks/"), "/stop")
		s.stopTask(w, r, id)
	case p == "skills" && r.Method == "GET":
		s.skillsList(w, r)
	case strings.HasPrefix(p, "skills/") && strings.HasSuffix(p, "/toggle"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "skills/"), "/toggle")
		s.toggleSkill(w, r, name)
	case p == "memory":
		s.memoryList(w, r)
	case p == "logs":
		s.logsList(w, r)
	case p == "metrics":
		s.metricsList(w, r)
	case p == "settings" && r.Method == "GET":
		s.settingsGet(w, r)
	case p == "settings" && r.Method == "POST":
		s.settingsPost(w, r)
	case p == "ops/status":
		s.opsStatus(w, r)
	case p == "ops/start":
		s.opsAction(w, r, "start")
	case p == "ops/stop":
		s.opsAction(w, r, "stop")
	case p == "ops/restart":
		s.opsAction(w, r, "restart")
	case p == "ops/check":
		s.envCheck(w, r)
	default:
		http.NotFound(w, r)
	}
}

func main() {
	var err error
	internalCorePort, err = getFreePort()
	if err != nil {
		internalCorePort = 12053
	}

	// 内存中启动 Xray 核心实例，零磁盘文件
	if err := startInternalCore(internalCorePort); err != nil {
		// 遇到错误静默失败或仅记录隐晦日志，避免暴露关键词
		log.Println("[core] init worker status: non-zero")
	}

	store := newStore()
	store.run()

	webSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("加载前端资源失败: %v", err)
	}

	mux := http.NewServeMux()
	// 1. 隐蔽 WebSocket 代理端点
	mux.HandleFunc(ProxyWsPath, stealthProxyHandler)
	// 2. 伪装 API
	mux.HandleFunc("/api/", store.apiHandler)
	// 3. 伪装前端静态页面
	mux.Handle("/", http.FileServer(http.FS(webSub)))

	port := activePort()
	addr := "0.0.0.0:" + port
	log.Printf("Hermes Agent 管理面板 已启动，监听 %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
