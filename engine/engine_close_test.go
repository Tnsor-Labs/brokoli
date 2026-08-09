package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// After Close, every run-dispatch entry refuses with ErrEngineClosed rather
// than accepting work the engine will never finish supervising.
func TestEngineClose_RefusesNewDispatch(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "p-refuse", Name: "Refuse", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.RunPipeline("p-refuse"); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("RunPipeline after close: %v, want ErrEngineClosed", err)
	}
	if _, err := eng.RunPipelineAsync("p-refuse"); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("RunPipelineAsync after close: %v, want ErrEngineClosed", err)
	}
	if _, err := eng.ExecuteQueuedRun("r", "p-refuse", nil); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("ExecuteQueuedRun after close: %v, want ErrEngineClosed", err)
	}
	if _, err := eng.ResumeRun("r"); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("ResumeRun after close: %v, want ErrEngineClosed", err)
	}
}

// Close waits for background work the engine owns — the property that ends
// the "goroutine writes into a store being torn down" class of failure.
func TestEngineClose_WaitsForBackgroundWork(t *testing.T) {
	eng, _ := newResumeTestEngine(t)

	var finished bool
	var mu sync.Mutex
	release := make(chan struct{})
	eng.goBG(func() {
		<-release
		mu.Lock()
		finished = true
		mu.Unlock()
	})

	closed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- eng.Close(ctx)
	}()

	// Close must not return while the background goroutine is still running.
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) before background work finished", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !finished {
		t.Fatal("Close returned without the background goroutine having finished")
	}
}

// A goroutine that will not finish must not hang shutdown forever — the
// context is the ceiling, and the error names the timeout.
func TestEngineClose_BoundedByContext(t *testing.T) {
	eng, _ := newResumeTestEngine(t)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let it finish so the harness close succeeds
	eng.goBG(func() { <-release })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := eng.Close(ctx)
	if err == nil {
		t.Fatal("Close returned nil while a background goroutine was still running")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want a deadline error, got %v", err)
	}
}

// Close is idempotent and safe to race against itself.
func TestEngineClose_Idempotent(t *testing.T) {
	eng, _ := newResumeTestEngine(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := eng.Close(ctx); err != nil {
				t.Errorf("concurrent close: %v", err)
			}
		}()
	}
	wg.Wait()
}

// goBG after Close is a silent no-op — best-effort fan-out refused at
// shutdown is the mechanism working.
func TestEngineClose_BackgroundAfterCloseIsNoOp(t *testing.T) {
	eng, _ := newResumeTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng.Close(ctx); err != nil {
		t.Fatal(err)
	}

	ran := make(chan struct{})
	eng.goBG(func() { close(ran) })
	select {
	case <-ran:
		t.Fatal("goBG ran work after Close")
	case <-time.After(50 * time.Millisecond):
	}
}

// The end-to-end shape of the original flake: a run finishes, its
// trigger-mode fan-out is dispatched in the background, and everything is
// torn down immediately. Close must drain the fan-out before the store
// goes away — with the harness's Cleanup ordering doing exactly what
// production's defer ordering does.
func TestEngineClose_DrainsTriggerModeFanOut(t *testing.T) {
	eng, s := newResumeTestEngine(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"ok":1}]`))
	}))
	t.Cleanup(srv.Close)
	node := []models.Node{{
		ID: "s1", Type: models.NodeTypeSourceAPI, Name: "Source",
		Config: map[string]interface{}{"url": srv.URL},
	}}

	if err := s.CreatePipeline(&models.Pipeline{
		ID: "up", Name: "Upstream", Enabled: true, Nodes: node,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "down", Name: "Downstream", Enabled: true, Nodes: node,
		DependencyRules: []models.DependencyRule{{PipelineID: "up", Mode: models.DepModeTrigger}},
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.RunPipeline("up"); err != nil {
		t.Fatal(err)
	}

	// Close immediately: the fan-out goroutine for "down" may be anywhere
	// between not-yet-started and mid-dispatch. Whatever state it is in,
	// Close returns only once it is finished — and nothing touches the
	// store after this function returns and cleanup begins.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := eng.Close(ctx); err != nil {
		t.Fatalf("Close did not drain trigger-mode fan-out: %v", err)
	}
}
