package web

import (
	"net/http"
	"strings"
	"testing"
)

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

// Проба обязана считать то, что человек ввёл в поле примера. Поле стоит в той
// же форме, что и связка, и отправляется вместе с ней: стоило ему выпасть из
// формы — и проба на любой ввод отвечала «сообщение пустое», а понять по
// странице, почему, было нельзя.
func TestPreviewUsesSample(t *testing.T) {
	_, h := testHandler(t)

	rec := post(h, "/links/preview",
		"sample=on&direction=in&topic=shellies/a/relay/0&target=563:1&encode=byte&extract=raw")

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if strings.Contains(body, "сообщение пустое") {
		t.Fatalf("проба не увидела пример сообщения:\n%s", body)
	}
	// "on" в однобайтовом элементе — это единица, байт 01.
	if !strings.Contains(body, "01") {
		t.Errorf("в ответе нет байта 01:\n%s", body)
	}
}
