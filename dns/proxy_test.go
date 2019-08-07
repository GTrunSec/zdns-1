package dns

import (
	"fmt"
	"io/ioutil"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/mpolden/zdns/cache"
	"github.com/mpolden/zdns/log"
)

type dnsWriter struct{ lastReply *dns.Msg }

func (w *dnsWriter) LocalAddr() net.Addr         { return nil }
func (w *dnsWriter) RemoteAddr() net.Addr        { return nil }
func (w *dnsWriter) Write(b []byte) (int, error) { return 0, nil }
func (w *dnsWriter) Close() error                { return nil }
func (w *dnsWriter) TsigStatus() error           { return nil }
func (w *dnsWriter) TsigTimersOnly(b bool)       {}
func (w *dnsWriter) Hijack()                     {}

func (w *dnsWriter) WriteMsg(m *dns.Msg) error {
	w.lastReply = m
	return nil
}

type resolver struct {
	answer *dns.Msg
	fail   bool
}

type testClient map[string]*resolver

func (c testClient) Exchange(m *dns.Msg, addr string) (*dns.Msg, time.Duration, error) {
	r, ok := c[addr]
	if !ok {
		panic("no such resolver: " + addr)
	}
	if r.fail {
		return nil, 0, fmt.Errorf("%s SERVFAIL", addr)
	}
	return r.answer, time.Minute * 5, nil
}

func testProxy(t *testing.T) *Proxy {
	return testProxyWithOptions(t, ProxyOptions{CacheExpiryInterval: time.Minute})
}

func testProxyWithOptions(t *testing.T, options ProxyOptions) *Proxy {
	log, err := log.New(ioutil.Discard, "", "")
	if err != nil {
		t.Fatal(err)
	}
	options.Logger = log
	proxy, err := NewProxy(options)
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func assertRR(t *testing.T, p *Proxy, m *dns.Msg, answer string) {
	var (
		qtype = m.Question[0].Qtype
		qname = m.Question[0].Name
	)
	w := &dnsWriter{}
	p.ServeDNS(w, m)

	qtypeString := dns.TypeToString[qtype]
	answers := w.lastReply.Answer
	if got, want := len(answers), 1; got != want {
		t.Fatalf("len(msg.Answer) = %d, want %d for %s %s", got, want, qtypeString, qname)
	}
	ans := answers[0]

	if got := w.lastReply.Id; got != m.Id {
		t.Errorf("id = %d, want %d for %s %s", got, m.Id, qtypeString, qname)
	}

	want := net.ParseIP(answer)
	var got net.IP
	switch qtype {
	case dns.TypeA:
		rr, ok := ans.(*dns.A)
		if !ok {
			t.Errorf("type = %q, want %q for %s %s", dns.TypeToString[dns.TypeA], dns.TypeToString[rr.Header().Rrtype], qtypeString, qname)
		}
		got = rr.A
	case dns.TypeAAAA:
		rr, ok := ans.(*dns.AAAA)
		if !ok {
			t.Errorf("type = %q, want %q for %s %s", dns.TypeToString[dns.TypeA], dns.TypeToString[rr.Header().Rrtype], qtypeString, qname)
		}
		got = rr.AAAA
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("IP = %s, want %s", got, want)
	}
}

func assertFailure(t *testing.T, p *Proxy, rtype uint16, qname string) {
	m := dns.Msg{}
	m.Id = dns.Id()
	m.RecursionDesired = true
	m.SetQuestion(dns.Fqdn(qname), rtype)

	w := &dnsWriter{}
	p.ServeDNS(w, &m)

	if got, want := len(w.lastReply.Answer), 0; got != want {
		t.Errorf("len(msg.Answer) = %d, want %d for %s %s", got, want, dns.TypeToString[rtype], qname)
	}
	if got, want := w.lastReply.MsgHdr.Rcode, dns.RcodeServerFailure; got != want {
		t.Errorf("MsgHdr.Rcode = %s, want %s for %s %s", dns.RcodeToString[got], dns.RcodeToString[want], dns.TypeToString[rtype], qname)
	}
}

func TestProxy(t *testing.T) {
	var h Handler = func(r *Request) *Reply {
		switch r.Type {
		case TypeA:
			return ReplyA(r.Name, net.IPv4zero)
		case TypeAAAA:
			return ReplyAAAA(r.Name, net.IPv6zero)
		}
		return nil
	}
	p := testProxy(t)
	p.handler = h

	m := dns.Msg{}
	m.Id = dns.Id()
	m.RecursionDesired = true

	m.SetQuestion(dns.Fqdn("badhost1"), dns.TypeA)
	assertRR(t, p, &m, "0.0.0.0")

	m.SetQuestion(dns.Fqdn("badhost1"), dns.TypeAAAA)
	assertRR(t, p, &m, "::")
}

func TestProxyWithResolvers(t *testing.T) {
	p := testProxy(t)
	p.resolvers = []string{"resolver1"}
	client := make(testClient)
	p.client = client

	// First and only resolver responds succesfully
	reply := ReplyA("host1", net.ParseIP("192.0.2.1"))
	m := dns.Msg{}
	m.Id = dns.Id()
	m.SetQuestion("host1.", dns.TypeA)
	m.Answer = reply.rr
	client["resolver1"] = &resolver{answer: &m}
	assertRR(t, p, &m, "192.0.2.1")

	// First and only resolver fails
	client["resolver1"].fail = true
	assertFailure(t, p, TypeA, "host1")

	// First resolver fails, but second succeeds
	reply = ReplyA("host1", net.ParseIP("192.0.2.2"))
	p.resolvers = []string{"resolver1", "resolver2"}
	m = dns.Msg{}
	m.Id = dns.Id()
	m.SetQuestion("host1.", dns.TypeA)
	m.Answer = reply.rr
	client["resolver2"] = &resolver{answer: &m}
	assertRR(t, p, &m, "192.0.2.2")

	// All resolvers fail
	client["resolver2"].fail = true
	assertFailure(t, p, TypeA, "host1")
}

func TestProxyWithCache(t *testing.T) {
	p := testProxyWithOptions(t, ProxyOptions{CacheSize: 10, CacheExpiryInterval: time.Minute})
	p.resolvers = []string{"resolver1"}
	client := make(testClient)
	p.client = client

	reply := ReplyA("host1", net.ParseIP("192.0.2.1"))
	m := dns.Msg{}
	m.Id = dns.Id()
	m.SetQuestion("host1.", dns.TypeA)
	m.Answer = reply.rr
	client["resolver1"] = &resolver{answer: &m}
	assertRR(t, p, &m, "192.0.2.1")

	k := cache.NewKey("host1.", dns.TypeA, dns.ClassINET)
	got, ok := p.cache.Get(k)
	if !ok {
		t.Errorf("cache.Get(%d) = (%+v, %t), want (%+v, %t)", k, got, ok, m, !ok)
	}
}

func TestReplyString(t *testing.T) {
	var tests = []struct {
		fn      func(string, ...net.IP) *Reply
		fnName  string
		name    string
		ipAddrs []net.IP
		out     string
	}{
		{ReplyA, "ReplyA", "test-host", []net.IP{net.ParseIP("192.0.2.1")},
			"test-host\t3600\tIN\tA\t192.0.2.1"},
		{ReplyA, "ReplyA", "test-host", []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")},
			"test-host\t3600\tIN\tA\t192.0.2.1\ntest-host\t3600\tIN\tA\t192.0.2.2"},
		{ReplyAAAA, "ReplyAAAA", "test-host", []net.IP{net.ParseIP("2001:db8::1")},
			"test-host\t3600\tIN\tAAAA\t2001:db8::1"},
		{ReplyAAAA, "ReplyAAAA", "test-host", []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2")},
			"test-host\t3600\tIN\tAAAA\t2001:db8::1\ntest-host\t3600\tIN\tAAAA\t2001:db8::2"},
	}
	for i, tt := range tests {
		got := tt.fn(tt.name, tt.ipAddrs...).String()
		if got != tt.out {
			t.Errorf("#%d: %s(%q, %v) = %q, want %q", i, tt.fnName, tt.name, tt.ipAddrs, got, tt.out)
		}
	}
}
