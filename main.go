// License: MIT
// Copyright (c) 2025 qualialog
package main

import (
	"bytes"
	"encoding/csv"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ledgerPath = "/tmp/main.journal"

var (
	expenseGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_expenses",
			Help: "Expenses per category and currency",
		},
		[]string{"category", "currency"},
	)

	assetGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_assets",
			Help: "Assets per account and currency",
		},
		[]string{"account", "currency"},
	)

	incomeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_income",
			Help: "Income per account and currency",
		},
		[]string{"account", "currency"},
	)

	ledgerTotalExpenses = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_total_expenses",
			Help: "Total expenses by currency",
		},
		[]string{"currency"},
	)

	ledgerTotalAssets = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_total_assets",
			Help: "Total assets by currency",
		},
		[]string{"currency"},
	)

	ledgerTotalIncome = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_total_income",
			Help: "Total income by currency",
		},
		[]string{"currency"},
	)

	ledgerExpensesMonthly = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_expenses_monthly",
			Help: "Monthly expenses by category, currency, and month",
		},
		[]string{"category", "currency", "month", "month_tag"},
	)

	ledgerExpenseByPayee = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ledger_expense_by_payee",
			Help: "Monthly aggregated expenses by normalized payee",
		},
		[]string{"payee", "currency", "month", "month_tag"},
	)
)

func parseAmount(s string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
}

func normalizePayee(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`\s*\(.*?\)\s*`).ReplaceAllString(s, "")
	return s
}

func fetchJournal() error {
	log.Println("fetchJournal called")
	token := os.Getenv("GITEA_TOKEN")
	url := os.Getenv("GITEA_JOURNAL_URL")
	if token == "" || url == "" {
		log.Println("missing GITEA_TOKEN or GITEA_JOURNAL_URL")
		return nil
	}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "token "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return os.WriteFile(ledgerPath, data, 0644)
}

func collectBalances(accountType string, gauge *prometheus.GaugeVec, prefixToTrim string) {
	log.Printf("collectBalances: %s", accountType)
	cmd := exec.Command("hledger", "-f", ledgerPath, "-s", "bal", accountType, "--depth", "5", "--no-elide")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		log.Printf("error running hledger for %s: %v\n%s", accountType, err, out.String())
		return
	}
	gauge.Reset()
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "----") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 1 && (strings.HasPrefix(line, "€") || strings.HasPrefix(line, "$")) {
			firstRune, runeSize := utf8.DecodeRuneInString(line)
			amountStr := line[runeSize:]
			amount, err := parseAmount(amountStr)
			if err != nil {
				log.Printf("could not parse total line %q: %v", line, err)
				continue
			}
			currency := map[string]string{"€": "EUR", "$": "USD"}[string(firstRune)]
			switch accountType {
			case "expenses":
				ledgerTotalExpenses.WithLabelValues(currency).Set(amount)
			case "assets":
				ledgerTotalAssets.WithLabelValues(currency).Set(amount)
			case "income":
				ledgerTotalIncome.WithLabelValues(currency).Set(amount)
			}
			continue
		}
		if len(parts) < 2 {
			log.Printf("skipping line: %q", line)
			continue
		}
		firstRune, runeSize := utf8.DecodeRuneInString(parts[0])
		amountStr := parts[0][runeSize:]
		amount, err := parseAmount(amountStr)
		if err != nil {
			log.Printf("could not parse amount %q: %v", parts[0], err)
			continue
		}
		currency := map[string]string{"€": "EUR", "$": "USD"}[string(firstRune)]
		account := strings.TrimPrefix(parts[1], prefixToTrim)
		gauge.WithLabelValues(account, currency).Set(amount)
	}
}

func collectMonthlyExpenses() {
	log.Println("collectMonthlyExpenses called")
	cmd := exec.Command("hledger", "-f", ledgerPath, "-s", "reg", "expenses", "--monthly", "--output-format", "csv")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		log.Printf("hledger reg failed: %v\nOutput:\n%s", err, out.String())
		return
	}
	r := csv.NewReader(strings.NewReader(out.String()))
	records, err := r.ReadAll()
	if err != nil {
		log.Printf("error reading csv output: %v", err)
		return
	}
	ledgerExpensesMonthly.Reset()

	now := time.Now()
	currentMonth := now.Format("2006-01")
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	previousMonth := firstOfThisMonth.AddDate(0, 0, -1).Format("2006-01")

	for i, rec := range records {
		if i == 0 || len(rec) < 6 {
			continue
		}
		month := rec[1][:7]
		category := strings.TrimPrefix(rec[4], "expenses:")
		amountStr := strings.TrimSpace(rec[5])
		if amountStr == "" {
			continue
		}
		firstRune, runeSize := utf8.DecodeRuneInString(amountStr)
		amountNum := amountStr[runeSize:]
		amount, err := parseAmount(amountNum)
		if err != nil {
			continue
		}
		currency := map[string]string{"€": "EUR", "$": "USD"}[string(firstRune)]

		monthTag := ""
		if month == currentMonth {
			monthTag = "current"
		} else if month == previousMonth {
			monthTag = "previous"
		}
		ledgerExpensesMonthly.WithLabelValues(category, currency, month, monthTag).Set(amount)
	}
}

func collectExpenseTotalsByPayee() {
	log.Println("collectExpenseTotalsByPayee called")
	cmd := exec.Command("hledger", "-f", ledgerPath, "print", "expenses", "--output-format", "csv")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		log.Printf("hledger print failed: %v\n%s", err, out.String())
		return
	}
	r := csv.NewReader(strings.NewReader(out.String()))
	records, err := r.ReadAll()
	if err != nil {
		log.Printf("error reading csv: %v", err)
		return
	}
	ledgerExpenseByPayee.Reset()
	totals := map[string]map[string]float64{}
	currencies := map[string]string{}
	now := time.Now()
	currentMonth := now.Format("2006-01")
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	previousMonth := firstOfThisMonth.AddDate(0, 0, -1).Format("2006-01")

	for i, rec := range records {
		if i == 0 || len(rec) < 12 {
			continue
		}

		dateStr := strings.TrimSpace(rec[1])              // date
		desc := normalizePayee(strings.TrimSpace(rec[5])) // description
		account := strings.TrimSpace(rec[7])              // account
		amountStr := strings.TrimSpace(rec[11])           // debit
		currencySymbol := strings.TrimSpace(rec[9])       // commodity column

		if !strings.HasPrefix(account, "expenses:") {
			continue
		}

		parsedDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil || amountStr == "" {
			continue
		}

		amountVal, err := parseAmount(amountStr)
		if err != nil || amountVal <= 0 {
			continue
		}

		currency := map[string]string{"€": "EUR", "$": "USD"}[currencySymbol]
		month := parsedDate.Format("2006-01")

		if _, ok := totals[desc]; !ok {
			totals[desc] = map[string]float64{}
		}
		totals[desc][month] += amountVal
		currencies[desc] = currency
	}

	for payee, monthMap := range totals {
		for month, amt := range monthMap {
			currency := currencies[payee]
			monthTag := ""
			if month == currentMonth {
				monthTag = "current"
			} else if month == previousMonth {
				monthTag = "previous"
			}
			ledgerExpenseByPayee.WithLabelValues(payee, currency, month, monthTag).Set(amt)
		}
	}
}

func updateMetrics() {
	log.Println("updateMetrics called")
	if err := fetchJournal(); err != nil {
		log.Printf("error fetching journal: %v", err)
	}
	collectBalances("expenses", expenseGauge, "expenses:")
	collectBalances("assets", assetGauge, "assets:")
	collectBalances("income", incomeGauge, "income:")
	collectMonthlyExpenses()
	collectExpenseTotalsByPayee()
}

func main() {
	log.Println("main starting")
	os.Setenv("LEDGER_FILE", ledgerPath)
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		expenseGauge,
		assetGauge,
		incomeGauge,
		ledgerTotalExpenses,
		ledgerTotalAssets,
		ledgerTotalIncome,
		ledgerExpensesMonthly,
		ledgerExpenseByPayee,
	)
	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	updateMetrics()
	go func() {
		for {
			time.Sleep(300 * time.Second)
			updateMetrics()
		}
	}()
	log.Println("Exporter listening on :9000")
	log.Fatal(http.ListenAndServe(":9000", nil))
}
