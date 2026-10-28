package nvml_test

import (
	"context"
	"testing"

	"github.com/tijani-web/meridian/internal/agent/health"
	"github.com/tijani-web/meridian/internal/agent/nvml"
)

type testDevice struct {
	uuid        string
	temp        uint32
	ecc         uint64
	xid         uint64
	fellOffBus  bool
}

func (d *testDevice) GetUUID() (string, error)          { return d.uuid, nil }
func (d *testDevice) GetTemperature() (uint32, error)   { return d.temp, nil }
func (d *testDevice) GetTotalEccErrors() (uint64, error) { return d.ecc, nil }
func (d *testDevice) GetLastXID() (uint64, error)       { return d.xid, nil }
func (d *testDevice) IsFellOffBus() bool                { return d.fellOffBus }

type testNVMLClient struct {
	devices []*testDevice
}

func (c *testNVMLClient) Init() error                     { return nil }
func (c *testNVMLClient) Shutdown() error                 { return nil }
func (c *testNVMLClient) DeviceGetCount() (int, error)    { return len(c.devices), nil }
func (c *testNVMLClient) DeviceGetHandleByIndex(i int) (nvml.NVMLDevice, error) {
	return c.devices[i], nil
}

func TestNVMLSource_Healthy(t *testing.T) {
	client := &testNVMLClient{
		devices: []*testDevice{
			{uuid: "GPU-1", temp: 65, ecc: 0, xid: 0},
			{uuid: "GPU-2", temp: 70, ecc: 0, xid: 0},
		},
	}

	source := nvml.NewSource(client, nvml.SourceConfig{})
	if source.Name() != "nvml" {
		t.Errorf("expected source name nvml, got %s", source.Name())
	}

	statuses, err := source.CheckHealth(context.Background())
	if err != nil {
		t.Fatalf("CheckHealth failed: %v", err)
	}

	if len(statuses) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(statuses))
	}

	for _, s := range statuses {
		if !s.Healthy {
			t.Errorf("expected GPU %s to be healthy, got %v", s.UUID, s)
		}
	}
}

func TestNVMLSource_FellOffBus(t *testing.T) {
	client := &testNVMLClient{
		devices: []*testDevice{
			{uuid: "GPU-1", temp: 60, ecc: 0, xid: 0},
			{uuid: "GPU-2", fellOffBus: true},
		},
	}

	source := nvml.NewSource(client, nvml.SourceConfig{})
	statuses, err := source.CheckHealth(context.Background())
	if err != nil {
		t.Fatalf("CheckHealth failed: %v", err)
	}

	if statuses[1].Healthy {
		t.Errorf("expected GPU 1 to be unhealthy due to bus drop")
	}
	if statuses[1].Severity != health.SeverityFatal {
		t.Errorf("expected SeverityFatal for bus drop, got %s", statuses[1].Severity)
	}
	if statuses[1].ErrorReason != "GPUFellOffBus" {
		t.Errorf("expected ErrorReason GPUFellOffBus, got %s", statuses[1].ErrorReason)
	}
}

func TestNVMLSource_ThermalAndECC(t *testing.T) {
	client := &testNVMLClient{
		devices: []*testDevice{
			{uuid: "GPU-Hot", temp: 96, ecc: 0, xid: 0},
			{uuid: "GPU-ECC", temp: 60, ecc: 4, xid: 0},
		},
	}

	source := nvml.NewSource(client, nvml.SourceConfig{
		MaxTempCelsius:      88.0,
		CriticalTempCelsius: 95.0,
	})
	statuses, err := source.CheckHealth(context.Background())
	if err != nil {
		t.Fatalf("CheckHealth failed: %v", err)
	}

	// GPU-Hot: Critical thermal shutdown
	if statuses[0].Healthy || statuses[0].Severity != health.SeverityFatal {
		t.Errorf("expected GPU-Hot to have SeverityFatal, got %v", statuses[0])
	}

	// GPU-ECC: Uncorrectable ECC
	if statuses[1].Healthy || statuses[1].Severity != health.SeverityFatal {
		t.Errorf("expected GPU-ECC to have SeverityFatal, got %v", statuses[1])
	}
}
