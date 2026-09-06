package httpapi

import "testing"

func TestParseListeningPorts(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   []int
	}{
		{name: "empty", output: "", want: nil},
		{name: "ss header only", output: "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process", want: nil},
		{name: "single localhost", output: "LISTEN 0 128 127.0.0.1:3000 0.0.0.0:*", want: []int{3000}},
		{name: "0.0.0.0 and :::", output: "LISTEN 0 128 0.0.0.0:3000 0.0.0.0:*\nLISTEN 0 128 :::4000 :::*", want: []int{3000, 4000}},
		{name: "duplicate ports", output: "LISTEN 0 128 127.0.0.1:3000 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:3000 0.0.0.0:*", want: []int{3000}},
		{name: "wildcard *", output: "*:5173", want: []int{5173}},
		{name: "netstat style", output: "tcp 0 0 127.0.0.1:8000 0.0.0.0:* LISTEN", want: []int{8000}},
		{name: "multiple distinct", output: "127.0.0.1:3000\n127.0.0.1:4000\n127.0.0.1:5173", want: []int{3000, 4000, 5173}},
		{name: "sidecar ports hidden", output: "LISTEN 0 128 127.0.0.1:5900 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:6080 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:9222 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:9223 0.0.0.0:*\nLISTEN 0 128 127.0.0.1:3000 0.0.0.0:*", want: []int{3000}},
		{name: "only sidecar ports", output: "LISTEN 0 128 0.0.0.0:6080 0.0.0.0:*", want: nil},
		{name: "docker embedded dns hidden", output: "LISTEN 0 4096 127.0.0.11:41801 0.0.0.0:*", want: nil},
		{name: "docker dns plus real server", output: "LISTEN 0 4096 127.0.0.11:41801 0.0.0.0:*\nLISTEN 0 128 127.0.0.1:3000 0.0.0.0:*", want: []int{3000}},
		{name: "port number equal to dns port on a real bind is kept", output: "LISTEN 0 128 127.0.0.1:41801 0.0.0.0:*", want: []int{41801}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseListeningPorts(tc.output)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for i, s := range got {
				if s["port"].(int) != tc.want[i] {
					t.Fatalf("port %d got %v want %d", i, s["port"], tc.want[i])
				}
			}
		})
	}
}
