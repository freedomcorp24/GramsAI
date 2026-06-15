package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Container struct {
	ID        string
	Port      int
	UserID    int64
	CreatedAt time.Time
	LastUsed  time.Time
}

type Manager struct {
	mu         sync.Mutex
	containers map[int64]*Container
	nextPort   int
	apiKey     string
	dockerURL  string
	image      string
	hostIP     string
}

func NewManager(dockerHost, apiKey, image string) (*Manager, error) {
	url := strings.Replace(dockerHost, "tcp://", "http://", 1)
	resp, err := http.Get(url + "/_ping")
	if err != nil {
		return nil, fmt.Errorf("docker ping: %w", err)
	}
	resp.Body.Close()
	log.Println("Docker connected:", dockerHost)
	host := strings.TrimPrefix(dockerHost, "tcp://")
	hostIP := strings.Split(host, ":")[0]
	return &Manager{
		containers: make(map[int64]*Container),
		nextPort:   5001,
		apiKey:     apiKey,
		dockerURL:  url,
		image:      image,
		hostIP:     hostIP,
	}, nil
}

func (m *Manager) DockerHostIP() string { return m.hostIP }

type createBody struct {
	Image      string     `json:"Image"`
	Cmd        []string   `json:"Cmd"`
	Env        []string   `json:"Env"`
	WorkingDir string     `json:"WorkingDir"`
	HostConfig hostConfig `json:"HostConfig"`
}
type hostConfig struct {
	PortBindings map[string][]portBinding `json:"PortBindings"`
	Binds        []string                 `json:"Binds"`
	Dns          []string                 `json:"Dns,omitempty"`
	NetworkMode  string                   `json:"NetworkMode,omitempty"`
}
type portBinding struct {
	HostPort string `json:"HostPort"`
}
type createResp struct {
	Id string `json:"Id"`
}

func (m *Manager) GetOrCreate(ctx context.Context, userID int64) (*Container, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.containers[userID]; ok {
		resp, err := http.Get(m.dockerURL + "/containers/" + c.ID + "/json")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			c.LastUsed = time.Now()
			return c, nil
		}
		if resp != nil { resp.Body.Close() }
		m.removeContainer(c.ID)
		delete(m.containers, userID)
	}
	port := m.nextPort
	m.nextPort++
	portStr := fmt.Sprintf("%d", port)
	name := fmt.Sprintf("oc-user-%d", userID)
	body := createBody{
		Image: m.image,
		Cmd: []string{"serve", "--hostname", "0.0.0.0", "--port", portStr},
		Env: []string{"OPENROUTER_API_KEY=" + m.apiKey},
		WorkingDir: "/workspace",
		HostConfig: hostConfig{
			PortBindings: map[string][]portBinding{portStr + "/tcp": {{HostPort: portStr}}},
			Dns: []string{"10.152.152.10"},
					NetworkMode: "host",
					Binds: []string{
				fmt.Sprintf("/data/opencode/user-%d:/workspace", userID),
				fmt.Sprintf("/data/opencode/user-%d/.local:/root/.local", userID),
				fmt.Sprintf("/data/opencode/user-%d/.config:/root/.config", userID),
			},
		},
	}
	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", m.dockerURL+"/containers/create?name="+name, strings.NewReader(string(jsonBody)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil { return nil, fmt.Errorf("create container: %w", err) }
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		m.removeContainer(name)
		time.Sleep(1 * time.Second)
		m.mu.Unlock()
		c, err := m.GetOrCreate(ctx, userID)
		m.mu.Lock()
		return c, err
	}
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create failed %d: %s", resp.StatusCode, string(b))
	}
	var cr createResp
	json.NewDecoder(resp.Body).Decode(&cr)
	startResp, err := http.Post(m.dockerURL+"/containers/"+cr.Id+"/start", "", nil)
	if err != nil { return nil, fmt.Errorf("start: %w", err) }
	startResp.Body.Close()
	c := &Container{ID: cr.Id, Port: port, UserID: userID, CreatedAt: time.Now(), LastUsed: time.Now()}
	m.containers[userID] = c
	log.Printf("Container started: user=%d port=%d id=%s", userID, port, cr.Id[:12])
	time.Sleep(3 * time.Second)
	return c, nil
}

func (m *Manager) removeContainer(idOrName string) {
	req, _ := http.NewRequest("DELETE", m.dockerURL+"/containers/"+idOrName+"?force=true", nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil { resp.Body.Close() }
}

func (m *Manager) Remove(ctx context.Context, userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.containers[userID]; ok {
		m.removeContainer(c.ID)
		delete(m.containers, userID)
	}
}

func (m *Manager) Cleanup(maxIdle time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for uid, c := range m.containers {
		if now.Sub(c.LastUsed) > maxIdle {
			m.removeContainer(c.ID)
			delete(m.containers, uid)
			log.Printf("Cleanup: removed idle container user=%d", uid)
		}
	}
}
