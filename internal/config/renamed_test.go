package config

import (
	"strings"
	"testing"
)

func TestARenamedKeyIsRefusedByItsNewName(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "the old pool table",
			body: "[slot.pools.chat-groups]\nenabled = true\nslots = [\"w0\"]\n",
			want: "slot.pools.chat-groups was renamed worker.pools.chat-groups in figaro 0.69",
		},
		{
			name: "the old list of a pool's units",
			body: strings.Replace(poolBody, "workers = ", "slots = ", 1),
			want: "worker.pools.chat-groups.slots was renamed worker.pools.chat-groups.workers in figaro 0.69",
		},
		{
			name: "the old listener table",
			body: "[door.tls]\nlisten = \"127.0.0.1:7002\"\n",
			want: "door.tls was renamed endpoint.tls in figaro 0.70",
		},
		{
			name: "the old web listener",
			body: "[door.web]\npath = \"/run/figaro/web.sock\"\n",
			want: "door.web was renamed endpoint.web in figaro 0.70",
		},
		{
			name: "a box's old socket",
			body: "[sandbox.boxes.dev]\nexec = \"/run/box/dev.sock\"\ndoor = \"/run/box/dev/endpoint.sock\"\n",
			want: "sandbox.boxes.dev.door was renamed sandbox.boxes.dev.endpoint in figaro 0.70",
		},
		{
			name: "a box's old host socket",
			body: "[sandbox.boxes.dev]\nexec = \"/run/box/dev.sock\"\nendpoint = \"/run/box/dev/endpoint.sock\"\nhost_door = \"/run/host/dev.sock\"\n",
			want: "sandbox.boxes.dev.host_door was renamed sandbox.boxes.dev.host_endpoint in figaro 0.70",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadBody(t, c.body, "rou2")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error saying %q", err, c.want)
			}
		})
	}
}

func TestAConfigWithNoRenamedKeyLoads(t *testing.T) {
	if _, err := loadBody(t, poolBody, "rou2"); err != nil {
		t.Fatal(err)
	}
}
