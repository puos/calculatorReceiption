package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// ─── Override ────────────────────────────────────────────────────────────────

type Override struct {
	File      string `json:"file"`
	AmountKRW int    `json:"amountKRW"`
	AccountNo int    `json:"accountNo"`
	Account   string `json:"account"`
	Purpose   string `json:"purpose"`
	Note      string `json:"note"`
}

func loadOverrides(dir string) map[string]Override {
	result := make(map[string]Override)
	data, err := os.ReadFile(filepath.Join(dir, "overrides.json"))
	if err != nil {
		return result
	}
	var overrides []Override
	if err := json.Unmarshal(data, &overrides); err != nil {
		return result
	}
	for _, o := range overrides {
		result[o.File] = o
	}
	return result
}

// ─── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	User          string         `json:"user"`
	Project       string         `json:"project"`
	Year          int            `json:"year"`
	CategoryRules []CategoryRule `json:"categoryRules"`
}

type CategoryRule struct {
	Keywords  []string `json:"keywords"`
	AccountNo int      `json:"accountNo"`
	Account   string   `json:"account"`
	Purpose   string   `json:"purpose"`
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// ─── Receipt ─────────────────────────────────────────────────────────────────

type Receipt struct {
	Date      time.Time
	Amount    int
	Merchant  string
	AccountNo int
	Account   string
	Purpose   string
	FilePath  string
	Warning   string
}

// ─── PDF Parsing ─────────────────────────────────────────────────────────────

var (
	reDate      = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`)
	reDateSlash = regexp.MustCompile(`(\d{4}/\d{2}/\d{2})`)
	reTotal     = regexp.MustCompile(`합계([\d,]+)원`)
	reMerchant  = regexp.MustCompile(`가맹점명(.{1,20}?)업종`)
	reBizType   = regexp.MustCompile(`업종(.{1,10}?)대표자명`)
	reUSD       = regexp.MustCompile(`([\d.]+)\s*USD`)
	reEngMerch  = regexp.MustCompile(`(?:Merchant name|merchantname)([A-Z][A-Z .+\-]{2,40}?)(?:\+1|\d{10}|국가|Nation)`)
)

func extractText(path string) string {
	f, r, err := pdf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var sb strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, _ := page.GetPlainText(nil)
		sb.WriteString(text)
	}
	return sb.String()
}

// compact removes all whitespace for regex matching
func compact(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func parseAmount(s string) int {
	n, _ := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	return n
}

// dateFromFilename extracts date from filenames like "4_01.pdf"
func dateFromFilename(name string, year int) (time.Time, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "_")
	if len(parts) >= 2 {
		month, err1 := strconv.Atoi(parts[0])
		day, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil && month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local), true
		}
	}
	return time.Time{}, false
}

func parsePDF(path string, year int) Receipt {
	rec := Receipt{FilePath: path}

	raw := extractText(path)
	if strings.TrimSpace(raw) == "" {
		rec.Warning = "텍스트 추출 불가 (이미지 PDF)"
		if d, ok := dateFromFilename(filepath.Base(path), year); ok {
			rec.Date = d
		}
		return rec
	}

	c := compact(raw) // 공백/개행 제거된 연속 문자열

	// ── 국내 카드 전표 (매출전표) ──
	if strings.Contains(c, "매출전표") || (strings.Contains(c, "합계") && strings.Contains(c, "가맹점명")) {
		if m := reDate.FindString(c); m != "" {
			rec.Date, _ = time.Parse("2006-01-02", m)
		}
		if m := reTotal.FindStringSubmatch(c); len(m) > 1 {
			rec.Amount = parseAmount(m[1])
		}
		if m := reMerchant.FindStringSubmatch(c); len(m) > 1 {
			rec.Merchant = m[1]
		}
		if m := reBizType.FindStringSubmatch(c); len(m) > 1 {
			rec.Merchant += "(" + m[1] + ")"
		}
		return rec
	}

	// ── 해외 승인 전표 ──
	if strings.Contains(c, "해외승인전표") {
		if m := reDateSlash.FindString(c); m != "" {
			rec.Date, _ = time.Parse("2006/01/02", m)
		}
		if m := reUSD.FindStringSubmatch(raw); len(m) > 1 {
			usd, _ := strconv.ParseFloat(m[1], 64)
			rec.Amount = 0
			rec.Warning = fmt.Sprintf("해외결제 USD %.2f - KRW 금액 수동 입력 필요", usd)
		}
		// 영문 가맹점명 (원본 raw 사용, 개행 제거는 하되 공백 유지)
		rawNoNewline := strings.ReplaceAll(strings.ReplaceAll(raw, "\n", " "), "\r", "")
		rawNoNewline = strings.Join(strings.Fields(rawNoNewline), "") // 공백도 제거
		if m := reEngMerch.FindStringSubmatch(rawNoNewline); len(m) > 1 {
			rec.Merchant = strings.TrimSpace(m[1])
		}
		return rec
	}

	// ── 파싱 불완전: 파일명에서 날짜만 추출 ──
	if d, ok := dateFromFilename(filepath.Base(path), year); ok {
		rec.Date = d
	}
	rec.Warning = "알 수 없는 PDF 형식"
	return rec
}

func applyCategory(rec *Receipt, rules []CategoryRule) {
	text := strings.ToUpper(rec.Merchant)
	for _, rule := range rules {
		for _, kw := range rule.Keywords {
			if strings.Contains(text, strings.ToUpper(kw)) {
				rec.AccountNo = rule.AccountNo
				rec.Account = rule.Account
				rec.Purpose = rule.Purpose
				return
			}
		}
	}
	// 기본값: 회의식대
	rec.AccountNo = 10
	rec.Account = "회의식대"
	rec.Purpose = "회의식대(중식비)"
}

// ─── Excel Generation ────────────────────────────────────────────────────────

func generateExcel(templatePath, outputPath string, receipts []Receipt, cfg Config, year, month int) error {
	f, err := excelize.OpenFile(templatePath)
	if err != nil {
		return fmt.Errorf("템플릿 열기 실패: %w", err)
	}
	defer f.Close()

	sheet := "첨부1.실행예산상세정산"

	// 제목 업데이트
	title := fmt.Sprintf("%d년 %d월분 법인(개인) 카드 정산서", year, month)
	_ = f.SetCellValue(sheet, "B2", title)

	// 팀원 업데이트
	_ = f.SetCellValue(sheet, "C5", cfg.User)

	// 기존 왼쪽 데이터 행 클리어 (B~H, 9~76행)
	for row := 9; row <= 76; row++ {
		for _, col := range []string{"B", "C", "D", "E", "F", "G", "H"} {
			_ = f.SetCellValue(sheet, fmt.Sprintf("%s%d", col, row), nil)
		}
	}

	// 새 데이터 입력
	for i, rec := range receipts {
		row := 9 + i
		if row > 76 {
			fmt.Printf("⚠️  행 초과: %d건 이후 데이터 생략됨\n", 68)
			break
		}
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), rec.Date.Format("2006.01.02"))
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), rec.AccountNo)
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), rec.Account)
		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), rec.Amount)
		_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), rec.Purpose)
		_ = f.SetCellValue(sheet, fmt.Sprintf("G%d", row), cfg.User)
		_ = f.SetCellValue(sheet, fmt.Sprintf("H%d", row), cfg.User+"(개인)")
	}

	// 계정별 합계 계산
	accountTotals := make(map[int]int)
	for _, rec := range receipts {
		accountTotals[rec.AccountNo] += rec.Amount
	}
	grandTotal := 0
	for _, amt := range accountTotals {
		grandTotal += amt
	}

	// 우측 N 열 계정 합계 업데이트
	// 계정NO X → 행 (8+X), 즉 account 1 = row 9, account 10 = row 18, account 65 = row 73
	for row := 9; row <= 76; row++ {
		accountNoStr, _ := f.GetCellValue(sheet, fmt.Sprintf("J%d", row))
		accountNo, err := strconv.Atoi(strings.TrimSpace(accountNoStr))
		if err != nil {
			continue
		}
		total := accountTotals[accountNo]
		_ = f.SetCellValue(sheet, fmt.Sprintf("N%d", row), total)
	}

	// 합계 행 업데이트
	_ = f.SetCellValue(sheet, "N77", grandTotal)
	_ = f.SetCellValue(sheet, "E86", grandTotal)

	return f.SaveAs(outputPath)
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		fmt.Println("사용법: calculator <입력폴더>")
		fmt.Println("예시:   calculator input\\04")
		os.Exit(1)
	}

	inputDir := os.Args[1]

	cfg, err := loadConfig("config.json")
	if err != nil {
		fmt.Println("config.json 로드 실패:", err)
		os.Exit(1)
	}
	if cfg.Year == 0 {
		cfg.Year = time.Now().Year()
	}

	// 폴더명에서 월 추출
	folderName := filepath.Base(inputDir)
	month, err := strconv.Atoi(strings.TrimLeft(folderName, "0"))
	if err != nil || month < 1 || month > 12 {
		fmt.Printf("폴더명 '%s'에서 월을 인식할 수 없습니다 (예: 04)\n", folderName)
		os.Exit(1)
	}

	// overrides.json 로드
	overrides := loadOverrides(inputDir)

	// 파일 분류
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		fmt.Println("폴더 읽기 실패:", err)
		os.Exit(1)
	}

	var templatePath string
	var pdfPaths []string

	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		full := filepath.Join(inputDir, name)

		switch {
		case ext == ".xlsx" && strings.Contains(name, "지출비상세정산서"):
			templatePath = full
		case ext == ".pdf" && !strings.Contains(name, "지출경비영수증"):
			// *_2.pdf 는 원화환산 증빙용 이미지 PDF → overrides.json 으로 처리, 별도 항목 제외
			base := strings.TrimSuffix(name, ext)
			if strings.HasSuffix(base, "_2") {
				continue
			}
			pdfPaths = append(pdfPaths, full)
		}
	}

	if templatePath == "" {
		fmt.Println("템플릿 Excel 파일(지출비상세정산서*.xlsx)을 찾을 수 없습니다")
		os.Exit(1)
	}

	fmt.Printf("📋 템플릿: %s\n", filepath.Base(templatePath))
	fmt.Printf("📂 PDF 파일 %d개 처리 중...\n\n", len(pdfPaths))

	// PDF 파싱 및 카테고리 적용
	var receipts []Receipt
	for _, path := range pdfPaths {
		rec := parsePDF(path, cfg.Year)

		// overrides.json 에서 금액/카테고리 덮어쓰기
		name := filepath.Base(path)
		if ov, ok := overrides[name]; ok {
			if ov.AmountKRW > 0 {
				rec.Amount = ov.AmountKRW
			}
			if ov.AccountNo > 0 {
				rec.AccountNo = ov.AccountNo
				rec.Account = ov.Account
				rec.Purpose = ov.Purpose
			}
			rec.Warning = fmt.Sprintf("overrides.json: %s", ov.Note)
		}

		// override 에 카테고리가 없을 때만 자동 분류
		if rec.AccountNo == 0 {
			applyCategory(&rec, cfg.CategoryRules)
		}

		status := "✅"
		if rec.Warning != "" {
			status = "⚠️ "
		}
		fmt.Printf("  %s [%s] %s  %s → %s(%d)  %s원",
			status,
			name,
			rec.Date.Format("01/02"),
			rec.Merchant,
			rec.Account,
			rec.AccountNo,
			formatKRW(rec.Amount),
		)
		if rec.Warning != "" {
			fmt.Printf("\n     → %s", rec.Warning)
		}
		fmt.Println()

		receipts = append(receipts, rec)
	}

	// 날짜순 정렬
	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i].Date.Before(receipts[j].Date)
	})

	// 계정별 소계 출력
	fmt.Println()
	accountTotals := make(map[int]struct {
		name  string
		total int
	})
	for _, r := range receipts {
		at := accountTotals[r.AccountNo]
		at.name = r.Account
		at.total += r.Amount
		accountTotals[r.AccountNo] = at
	}
	var accountNos []int
	for no := range accountTotals {
		accountNos = append(accountNos, no)
	}
	sort.Ints(accountNos)
	for _, no := range accountNos {
		at := accountTotals[no]
		fmt.Printf("  계정%02d %-10s : %s원\n", no, at.name, formatKRW(at.total))
	}

	grandTotal := 0
	for _, r := range receipts {
		grandTotal += r.Amount
	}
	fmt.Printf("  %-16s : %s원\n", "합계", formatKRW(grandTotal))

	// output 폴더 생성
	if err := os.MkdirAll("output", 0755); err != nil {
		fmt.Println("output 폴더 생성 실패:", err)
		os.Exit(1)
	}

	outputPath := filepath.Join("output",
		fmt.Sprintf("지출비상세정산서_%s_%d%02d.xlsx", cfg.User, cfg.Year, month))

	fmt.Printf("\n📝 Excel 생성 중...\n")
	if err := generateExcel(templatePath, outputPath, receipts, cfg, cfg.Year, month); err != nil {
		fmt.Println("Excel 생성 실패:", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 완료: %s\n", outputPath)
}

func formatKRW(n int) string {
	if n == 0 {
		return "0"
	}
	s := strconv.Itoa(n)
	var result []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, c)
	}
	return string(result)
}
