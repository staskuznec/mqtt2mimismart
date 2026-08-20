package web

import "testing"

// Таблица значений вводится текстом, и порог «<=-90» сам содержит знак
// равенства. Если резать строку по первому «=», ключом станет «<», порог
// потеряется молча — а связка начнёт отдавать сырое число вместо слова.
func TestValuesFromTextKeepsThresholds(t *testing.T) {
	values, err := valuesFromText("<=-90 = нет сигнала\n<=-70 = средний\n>=80 = полный\n* = отличный\non = 1\n")
	if err != nil {
		t.Fatalf("valuesFromText: %v", err)
	}

	want := map[string]string{
		"<=-90": "нет сигнала",
		"<=-70": "средний",
		">=80":  "полный",
		"*":     "отличный",
		"on":    "1",
	}
	if len(values) != len(want) {
		t.Fatalf("разобрано %d пар, ожидалось %d: %v", len(values), len(want), values)
	}
	for key, text := range want {
		if values[key] != text {
			t.Errorf("ключ %q = %q, ожидалось %q", key, values[key], text)
		}
	}
}

// Строка без разделителя — ошибка с номером строки, и один только оператор
// порога разделителем не считается.
func TestValuesFromTextNeedsSeparator(t *testing.T) {
	if _, err := valuesFromText("on = 1\n<=-90\n"); err == nil {
		t.Fatal("строка без «=» принята, ожидалась ошибка")
	}
}

// Таблица переживает правку туда-обратно: то, что показано в форме, должно
// разобраться обратно в тот же вид.
func TestValuesTextRoundTrip(t *testing.T) {
	values := map[string]string{"<=-90": "нет сигнала", ">=80": "полный", "*": "отличный"}

	back, err := valuesFromText(valuesToText(values))
	if err != nil {
		t.Fatalf("valuesFromText: %v", err)
	}
	for key, text := range values {
		if back[key] != text {
			t.Errorf("ключ %q = %q, ожидалось %q", key, back[key], text)
		}
	}
}
