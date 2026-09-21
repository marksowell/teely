package teely

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestRestartButtonDisabledWhenStopped(t *testing.T) {
	for _, status := range []AppStatus{StatusStopped, StatusRunning, StatusStarting, StatusError} {
		t.Run(string(status), func(t *testing.T) {
			w := httptest.NewRecorder()
			renderDashboard(w, dashboardView{Apps: []AppState{{Config: AppConfig{ID: "example"}, Status: status}}})
			button := regexp.MustCompile(`<button[^>]*>Restart</button>`).FindString(w.Body.String())
			if button == "" || strings.Contains(button, "disabled") != (status == StatusStopped) {
				t.Fatalf("unexpected restart button for %s: %s", status, button)
			}
		})
	}
}
