/*
Package main implements the Meridian simulated GPU fault injector.

Per AGENTS.md §1.1, test infrastructure under tools/ must speak the real protocol
and be labeled "simulated". This binary provides an HTTP server exposing simulated
NVIDIA DCGM Prometheus metrics on /metrics, with dynamic fault injection APIs:
  POST /inject?gpu=0&fault=xid79 (bus drop)
  POST /inject?gpu=0&fault=xid62 (internal MCU halt)
  POST /inject?gpu=0&fault=ecc   (uncorrectable DBE)
  POST /inject?gpu=0&fault=thermal (temperature spike to 99C)
  POST /clear                    (restore all GPUs to healthy)
*/
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// SimulatedGPUState models the simulated hardware health of an individual device.
type SimulatedGPUState struct {
	Index    int
	UUID     string
	Temp     float64
	XID      int
	ECCDBE   int64
	ThermalV int
}

type FaultInjector struct {
	mu   sync.Mutex
	gpus map[int]*SimulatedGPUState
}

func NewFaultInjector(gpuCount int) *FaultInjector {
	gpus := make(map[int]*SimulatedGPUState)
	for i := 0; i < gpuCount; i++ {
		gpus[i] = &SimulatedGPUState{
			Index: i,
			UUID:  fmt.Sprintf("GPU-SIMULATED-%04d", i),
			Temp:  65.0,
		}
	}
	return &FaultInjector{gpus: gpus}
}

func (fi *FaultInjector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/metrics":
		fi.handleMetrics(w, r)
	case "/inject":
		fi.handleInject(w, r)
	case "/clear":
		fi.handleClear(w, r)
	case "/health":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK (simulated)"))
	default:
		http.NotFound(w, r)
	}
}

func (fi *FaultInjector) handleMetrics(w http.ResponseWriter, r *http.Request) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprintln(w, "# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature in Celsius (simulated).")
	fmt.Fprintln(w, "# TYPE DCGM_FI_DEV_GPU_TEMP gauge")
	for i := 0; i < len(fi.gpus); i++ {
		g := fi.gpus[i]
		fmt.Fprintf(w, "DCGM_FI_DEV_GPU_TEMP{gpu=\"%d\",UUID=\"%s\",mode=\"simulated\"} %.1f\n", g.Index, g.UUID, g.Temp)
	}

	fmt.Fprintln(w, "# HELP DCGM_FI_DEV_XID_ERRORS Value of last reported XID error (simulated).")
	fmt.Fprintln(w, "# TYPE DCGM_FI_DEV_XID_ERRORS gauge")
	for i := 0; i < len(fi.gpus); i++ {
		g := fi.gpus[i]
		fmt.Fprintf(w, "DCGM_FI_DEV_XID_ERRORS{gpu=\"%d\",UUID=\"%s\",mode=\"simulated\"} %d\n", g.Index, g.UUID, g.XID)
	}

	fmt.Fprintln(w, "# HELP DCGM_FI_DEV_ECC_DBE_VOL_TOTAL Total volatile double bit errors (simulated).")
	fmt.Fprintln(w, "# TYPE DCGM_FI_DEV_ECC_DBE_VOL_TOTAL counter")
	for i := 0; i < len(fi.gpus); i++ {
		g := fi.gpus[i]
		fmt.Fprintf(w, "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL{gpu=\"%d\",UUID=\"%s\",mode=\"simulated\"} %d\n", g.Index, g.UUID, g.ECCDBE)
	}

	fmt.Fprintln(w, "# HELP DCGM_FI_DEV_THERMAL_VIOLATION Thermal throttling violation (simulated).")
	fmt.Fprintln(w, "# TYPE DCGM_FI_DEV_THERMAL_VIOLATION gauge")
	for i := 0; i < len(fi.gpus); i++ {
		g := fi.gpus[i]
		fmt.Fprintf(w, "DCGM_FI_DEV_THERMAL_VIOLATION{gpu=\"%d\",UUID=\"%s\",mode=\"simulated\"} %d\n", g.Index, g.UUID, g.ThermalV)
	}
}

func (fi *FaultInjector) handleInject(w http.ResponseWriter, r *http.Request) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	gpuStr := r.URL.Query().Get("gpu")
	gpuIdx, _ := strconv.Atoi(gpuStr)
	g, exists := fi.gpus[gpuIdx]
	if !exists {
		http.Error(w, fmt.Sprintf("invalid gpu index %d", gpuIdx), http.StatusBadRequest)
		return
	}

	fault := r.URL.Query().Get("fault")
	switch fault {
	case "xid79", "bus_drop":
		g.XID = 79
	case "xid62":
		g.XID = 62
	case "xid54":
		g.XID = 54
	case "ecc", "dbe":
		g.ECCDBE += 5
	case "thermal":
		g.Temp = 99.0
		g.ThermalV = 1
	default:
		http.Error(w, fmt.Sprintf("unknown fault type: %s", fault), http.StatusBadRequest)
		return
	}

	log.Printf("[SIMULATED] Injected fault %s onto GPU %d (%s)", fault, gpuIdx, g.UUID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Injected %s onto GPU %d (simulated)\n", fault, gpuIdx)
}

func (fi *FaultInjector) handleClear(w http.ResponseWriter, r *http.Request) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	for _, g := range fi.gpus {
		g.Temp = 65.0
		g.XID = 0
		g.ECCDBE = 0
		g.ThermalV = 0
	}

	log.Println("[SIMULATED] Cleared all GPU fault states; restored to healthy")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "All simulated GPUs restored to healthy")
}

func main() {
	port := flag.Int("port", 9400, "Port for the simulated DCGM metrics server")
	gpuCount := flag.Int("gpus", 8, "Number of simulated physical GPUs on this node")
	flag.Parse()

	injector := NewFaultInjector(*gpuCount)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      injector,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	log.Printf("Starting Meridian GPU Fault Injector on :%d with %d simulated GPUs...", *port, *gpuCount)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
