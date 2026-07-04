package main

import "testing"

func TestCapAmount(t *testing.T) {
	rules := []CategoryRule{
		{Keywords: []string{"한식"}, AccountNo: 10, Account: "회의식대", Purpose: "회의식대(중식비)", MaxAmount: 10000},
	}

	// 규칙 매칭 + 상한 초과
	rec := Receipt{Merchant: "바른식탁(한식)", Amount: 12000}
	applyCategory(&rec, rules)
	if rec.Amount != 10000 {
		t.Errorf("규칙 매칭 상한: got %d, want 10000", rec.Amount)
	}
	if rec.Warning == "" {
		t.Error("상한 적용 시 Warning이 기록되어야 함")
	}

	// 규칙 매칭 + 상한 이하 → 그대로
	rec = Receipt{Merchant: "바른식탁(한식)", Amount: 7500}
	applyCategory(&rec, rules)
	if rec.Amount != 7500 || rec.Warning != "" {
		t.Errorf("상한 이하: got %d (warning=%q), want 7500 그대로", rec.Amount, rec.Warning)
	}

	// 매칭 실패 → 기본 회의식대 + 기본 상한
	rec = Receipt{Merchant: "알수없는가맹점", Amount: 15000}
	applyCategory(&rec, rules)
	if rec.AccountNo != 10 || rec.Amount != 10000 {
		t.Errorf("기본 분류 상한: got 계정%d %d원, want 계정10 10000원", rec.AccountNo, rec.Amount)
	}

	// 상한 없는 규칙(MaxAmount=0) → 무제한
	rules2 := []CategoryRule{
		{Keywords: []string{"CLAUDE"}, AccountNo: 65, Account: "재료비", Purpose: "AI 도구"},
	}
	rec = Receipt{Merchant: "CLAUDE.AI", Amount: 33965}
	applyCategory(&rec, rules2)
	if rec.Amount != 33965 {
		t.Errorf("상한 없음: got %d, want 33965", rec.Amount)
	}
}

func TestFormatKRWHangul(t *testing.T) {
	cases := map[int]string{
		0:         "영",
		7500:      "칠천오백",
		10000:     "만",
		15000:     "만오천",
		33965:     "삼만삼천구백육십오",
		60000:     "육만",
		93607:     "구만삼천육백칠",
		144965:    "십사만사천구백육십오",
		100000000: "일억",
		100000010: "일억십",
	}
	for n, want := range cases {
		if got := formatKRWHangul(n); got != want {
			t.Errorf("formatKRWHangul(%d) = %q, want %q", n, got, want)
		}
	}
}
