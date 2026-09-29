package logic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestInertialSwallowsNarrowPulse 故障单场景：
// 延迟 3 的反相门输入一段 t=1..2 的窄脉冲（宽 1 < 延迟 3）。
// 惯性模式下候选（w=0 @4）在到期前因输入回落而失效，输出必须保持安静；
// 同一请求切回 transport 则脉冲穿过（t=4、t=5 跳变）——两种模式必须不同。
func TestInertialSwallowsNarrowPulse(t *testing.T) {
	mk := func(model string) *Request {
		return &Request{
			Inputs:      []Input{{ID: "a", Init: false, Events: []Event{{At: 1}, {At: 2}}}},
			Gates:       []Gate{gate(NOT, "w", 3, "a")},
			Observe:     []string{"w"},
			GlitchWidth: 2,
			DelayModel:  model,
		}
	}

	c, err := compile(mk("inertial"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res := c.simulate()
	assertEdges(t, c, res, "w", nil)
	if resp := c.buildResponse(res); len(resp.Pulses) != 0 {
		t.Fatalf("惯性模式不应把被吞的窄脉冲报为穿过门的信号: %+v", resp.Pulses)
	}

	// 传输模式对照：脉冲穿过，t=4 变 0、t=5 变回 1，并报出宽 1 负脉冲。
	ct, err := compile(mk("transport"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rest := ct.simulate()
	assertEdges(t, ct, rest, "w", []Jump{{At: 4, Value: false}, {At: 5, Value: true}})
	respt := ct.buildResponse(rest)
	if len(respt.Pulses) != 1 || respt.Pulses[0].Width != 1 || respt.Pulses[0].Value {
		t.Fatalf("传输模式应检出一个宽度1的低电平脉冲, got %+v", respt.Pulses)
	}
}

// TestInertialWidthEqualsDelayPasses 边界：脉冲宽度恰等于门延迟时穿过。
// 外部翻转恰在候选到期时刻发生，按同刻批量语义处理：
// 候选先生效，随后依据批量后的输入重算并排新候选。
func TestInertialWidthEqualsDelayPasses(t *testing.T) {
	req := &Request{
		Inputs:      []Input{{ID: "a", Init: false, Events: []Event{{At: 1}, {At: 4}}}},
		Gates:       []Gate{gate(NOT, "w", 3, "a")},
		Observe:     []string{"w"},
		GlitchWidth: 4,
		DelayModel:  "inertial",
	}
	c, err := compile(req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res := c.simulate()
	// t=1 排候选 w=0 @4；t=4 候选到期与 a 回落同刻：候选生效 w=0，
	// 重算得 1 排 @7；t=7 w 变回 1。宽 3 的负脉冲完整穿过。
	assertEdges(t, c, res, "w", []Jump{{At: 4, Value: false}, {At: 7, Value: true}})
	resp := c.buildResponse(res)
	if len(resp.Pulses) != 1 || resp.Pulses[0].From != 4 || resp.Pulses[0].To != 7 || resp.Pulses[0].Value {
		t.Fatalf("应检出一个宽度3的低电平脉冲, got %+v", resp.Pulses)
	}
}

// TestInertialCandidatePersists 候选值持续成立则不重新计时：
// OR 门 a@1、b@2 先后变 1，候选值 1 从 t=1 起持续成立，输出在 t=4（而非 t=5）变高。
// AND 门对称：a@1、b@2 先后变 0，输出在 t=4 变低。
func TestInertialCandidatePersists(t *testing.T) {
	req := &Request{
		Inputs: []Input{
			{ID: "a", Init: false, Events: []Event{{At: 1}}},
			{ID: "b", Init: false, Events: []Event{{At: 2}}},
		},
		Gates:      []Gate{gate(OR, "z", 3, "a", "b")},
		Observe:    []string{"z"},
		DelayModel: "inertial",
	}
	c, err := compile(req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	assertEdges(t, c, c.simulate(), "z", []Jump{{At: 4, Value: true}})

	req2 := &Request{
		Inputs: []Input{
			{ID: "a", Init: true, Events: []Event{{At: 1}}},
			{ID: "b", Init: true, Events: []Event{{At: 2}}},
		},
		Gates:      []Gate{gate(AND, "z", 3, "a", "b")},
		Observe:    []string{"z"},
		DelayModel: "inertial",
	}
	c2, err := compile(req2)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	assertEdges(t, c2, c2.simulate(), "z", []Jump{{At: 4, Value: false}})
}

// TestInertialMultiStageFilter 多级门各自独立过滤：
// 宽 2 的脉冲穿过延迟 1 的第一级，被延迟 3 的第二级吞掉。
func TestInertialMultiStageFilter(t *testing.T) {
	req := &Request{
		Inputs: []Input{{ID: "a", Init: false, Events: []Event{{At: 1}, {At: 3}}}},
		Gates: []Gate{
			gate(NOT, "g1", 1, "a"),
			gate(NOT, "g2", 3, "g1"),
		},
		Observe:     []string{"g1", "g2"},
		GlitchWidth: 3,
		DelayModel:  "inertial",
	}
	c, err := compile(req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res := c.simulate()
	assertEdges(t, c, res, "g1", []Jump{{At: 2, Value: false}, {At: 4, Value: true}})
	assertEdges(t, c, res, "g2", nil)
	resp := c.buildResponse(res)
	// g1 上的宽 2 负脉冲如实上报；g2 无跳变、无脉冲。
	if len(resp.Pulses) != 1 || resp.Pulses[0].Net != "g1" || resp.Pulses[0].Width != 2 {
		t.Fatalf("脉冲报告异常: %+v", resp.Pulses)
	}
}

// TestInertialSameTickCascade 同刻级联：宽 3 脉冲穿过两级，
// 第一级的回落事件与第二级的上升事件同刻（t=5）到期，批量生效后再重算。
func TestInertialSameTickCascade(t *testing.T) {
	req := &Request{
		Inputs: []Input{{ID: "a", Init: false, Events: []Event{{At: 1}, {At: 4}}}},
		Gates: []Gate{
			gate(NOT, "g1", 1, "a"),
			gate(NOT, "g2", 3, "g1"),
		},
		Observe:    []string{"g1", "g2"},
		DelayModel: "inertial",
	}
	c, err := compile(req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res := c.simulate()
	assertEdges(t, c, res, "g1", []Jump{{At: 2, Value: false}, {At: 5, Value: true}})
	assertEdges(t, c, res, "g2", []Jump{{At: 5, Value: true}, {At: 8, Value: false}})
}

// TestInertialSameValueStaysQuiet 候选值等于当前门输出时不制造跳变：
// hold 恒 1，z=OR(a,hold) 恒 1；a 的翻转到头来重算结果都等于当前输出。
func TestInertialSameValueStaysQuiet(t *testing.T) {
	req := &Request{
		Inputs: []Input{
			{ID: "a", Init: false, Events: []Event{{At: 3}, {At: 5}}},
			{ID: "b", Init: true},
		},
		Gates: []Gate{
			gate(NOT, "nb", 1, "b"),
			gate(OR, "hold", 1, "b", "nb"),
			gate(OR, "z", 2, "a", "hold"),
		},
		Observe:    []string{"z"},
		DelayModel: "inertial",
	}
	c, err := compile(req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res := c.simulate()
	assertEdges(t, c, res, "z", nil)
	if !res.initV[c.idIndex["z"]] {
		t.Fatalf("z 初态应为 1")
	}
}

// TestCrossCheckInertialHandcrafted 惯性模式手工电路对拍。
func TestCrossCheckInertialHandcrafted(t *testing.T) {
	cases := []*Request{
		// 重汇毛刺：各门延迟 1，宽 1 毛刺 >= 延迟，惯性模式下照样穿过。
		{
			Inputs: []Input{
				{ID: "A", Init: false, Events: []Event{{At: 1}}},
				{ID: "B", Init: false},
			},
			Gates: []Gate{
				gate(NOT, "nb", 1, "B"),
				gate(OR, "hold", 1, "B", "nb"),
				gate(AND, "g1", 1, "A", "hold"),
				gate(XOR, "z", 1, "g1", "A"),
			},
			DelayModel: "inertial",
		},
		// 同刻抵消：两输入同拍翻转，重算结果不变，惯性候选无从产生。
		{
			Inputs: []Input{
				{ID: "p", Init: true, Events: []Event{{At: 1}}},
				{ID: "q", Init: true, Events: []Event{{At: 1}}},
				{ID: "r", Init: false, Events: []Event{{At: 1}, {At: 4}}},
			},
			Gates: []Gate{
				gate(XOR, "x", 2, "p", "q", "r"),
				gate(AND, "y", 3, "p", "r"),
				gate(OR, "zz", 1, "x", "y"),
			},
			DelayModel: "inertial",
		},
		// 延迟各异的多级链 + 末级汇合。
		{
			Inputs: []Input{{ID: "a", Init: false, Events: []Event{{At: 1}, {At: 2}, {At: 10}}}},
			Gates: []Gate{
				gate(NOT, "g1", 1, "a"),
				gate(NOT, "g2", 3, "g1"),
				gate(NOT, "g3", 2, "g2"),
				gate(AND, "g4", 8, "g3", "a"),
			},
			DelayModel: "inertial",
		},
		// 多输入门候选被部分输入回落提前失效。
		{
			Inputs: []Input{
				{ID: "a", Init: false, Events: []Event{{At: 1}, {At: 3}}},
				{ID: "b", Init: false, Events: []Event{{At: 2}, {At: 6}}},
			},
			Gates: []Gate{
				gate(AND, "z", 4, "a", "b"),
				gate(XOR, "w", 2, "a", "z"),
			},
			DelayModel: "inertial",
		},
	}
	for i, req := range cases {
		t.Run(fmt.Sprintf("case%d", i), func(t *testing.T) { crossCheck(t, req) })
	}
}

// TestCrossCheckInertialRandom 随机无环电路（含 NOT）惯性模式对拍。
func TestCrossCheckInertialRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(20260929))
	for iter := 0; iter < 400; iter++ {
		req := genRandomDAGWithNot(rng)
		req.DelayModel = "inertial"
		crossCheck(t, req)
	}
}

// TestPulsesCrossCheckInertial 惯性模式下用跳变边独立推算窄脉冲集合，
// 与 buildResponse 的输出对拍。
func TestPulsesCrossCheckInertial(t *testing.T) {
	rng := rand.New(rand.NewSource(777))
	for iter := 0; iter < 300; iter++ {
		req := genRandomDAGWithNot(rng)
		req.DelayModel = "inertial"
		req.GlitchWidth = 1 + rng.Intn(6)
		c, err := compile(req)
		if err != nil {
			t.Fatal(err)
		}
		res := c.simulate()
		req.Observe = append(req.Observe, c.names...)
		resp := c.buildResponse(res)

		type pk struct {
			net  string
			from int
		}
		wantM := map[pk]Pulse{}
		for idx, es := range res.edges {
			for i := 0; i+1 < len(es); i++ {
				w := es[i+1].At - es[i].At
				if w < req.GlitchWidth {
					p := Pulse{Net: c.names[idx], From: es[i].At, To: es[i+1].At, Width: w, Value: es[i].Value}
					wantM[pk{p.Net, p.From}] = p
				}
			}
		}
		gotM := map[pk]Pulse{}
		for _, p := range resp.Pulses {
			gotM[pk{p.Net, p.From}] = p
		}
		if !reflect.DeepEqual(gotM, wantM) {
			t.Fatalf("iter=%d 阈值=%d 惯性模式脉冲集合不一致:\n got %v\n want %v",
				iter, req.GlitchWidth, gotM, wantM)
		}
	}
}

// TestHTTPInertial 故障单的 HTTP 端到端回归：
// delay_model=inertial 时观察点不得在 4、5 跳变，脉冲列表必须为空。
func TestHTTPInertial(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	body := `{
	  "inputs": [{"id":"a","init":false,"events":[{"at":1},{"at":2}]}],
	  "gates": [{"type":"NOT","id":"w","delay":3,"inputs":["a"]}],
	  "observe": ["w"],
	  "glitch_width": 2,
	  "delay_model": "inertial"
	}`
	resp, err := http.Post(srv.URL+"/simulate", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Timelines) != 1 || out.Timelines[0].Net != "w" {
		t.Fatalf("时间线异常: %+v", out.Timelines)
	}
	if len(out.Timelines[0].Jumps) != 0 {
		t.Fatalf("惯性模式应吞掉窄脉冲, got jumps %+v", out.Timelines[0].Jumps)
	}
	if len(out.Pulses) != 0 {
		t.Fatalf("惯性模式不应上报脉冲, got %+v", out.Pulses)
	}
	if !out.Timelines[0].Init {
		t.Fatalf("w.init 应为 true")
	}
}
