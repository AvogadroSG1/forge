package main

import "testing"

func TestListenAddrReadsPortFromEnvironment(t *testing.T) {
	tests := []struct {
		name string
		port string
		want string
	}{
		{name: "defaults when unset", port: "", want: ":8080"},
		{name: "uses PORT", port: "9090", want: ":9090"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PORT", tt.port)

			if got := listenAddr(); got != tt.want {
				t.Fatalf("listenAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}
