package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnextmigration"
)

type priceMappings map[string]string

func (values priceMappings) String() string {
	items := make([]string, 0, len(values))
	for model, sku := range values {
		items = append(items, model+"="+sku)
	}
	return strings.Join(items, ",")
}

func (values priceMappings) Set(value string) error {
	model, sku, ok := strings.Cut(value, "=")
	model = strings.TrimSpace(model)
	sku = strings.TrimSpace(sku)
	if !ok || model == "" || sku == "" {
		return fmt.Errorf("price mapping must be MODEL=SKU")
	}
	values[model] = sku
	return nil
}

func main() {
	var source string
	var destination string
	var surface string
	prices := priceMappings{}
	flag.StringVar(&source, "source", "", "path to the read-only legacy SQLite database")
	flag.StringVar(&destination, "destination", "", "path for the new VNext SQLite database")
	flag.StringVar(&surface, "openai-surface", "", "required: chat, responses, or both")
	flag.Var(prices, "price", "official pricing override in MODEL=SKU form; repeat as needed")
	flag.Parse()
	if strings.TrimSpace(source) == "" || strings.TrimSpace(destination) == "" {
		fatal("--source and --destination are required")
	}
	masterKey, err := vnextmigration.ParseMasterKeyHex(os.Getenv("JIESHAN_SECRET_KEY"))
	if err != nil {
		fatal("JIESHAN_SECRET_KEY: %v", err)
	}
	result, err := vnextmigration.MigrateSQLiteFile(context.Background(), source, destination,
		vnextmigration.MigrationOptions{
			MasterKey: masterKey, OpenAISurface: vnextmigration.OpenAISurfacePolicy(strings.ToLower(strings.TrimSpace(surface))),
			PriceSKUOverrides: prices,
		})
	if err != nil {
		fatal("migration failed: %v", err)
	}
	encoded, err := vnextmigration.WriteMigrationResultJSON(result)
	if err != nil {
		fatal("encode migration result: %v", err)
	}
	fmt.Println(string(encoded))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
