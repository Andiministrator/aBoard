#!/usr/bin/env python3
# Erzeugt server/testdata/schatten/t16-vektoren.json (Karte #809, F-2017).
#
# Entwurfsgenerator von aBoard. Massgeblich, bis das Partnerprojekt die Datei uebernimmt
# (herkunft.commit gesetzt, stand ohne "Entwurf"); danach ist die JSON-Datei
# vom Partnerprojekt fuehrend, und dieses Skript ist nur noch Vorlage. Es setzt
# herkunft.commit immer auf den Entwurfs-Platzhalter: nie ueber eine
# uebernommene Datei laufen lassen, ohne herkunft von Hand zu erhalten.
# Aufruf: python3 server/testdata/schatten/erzeuge-t16.py <ziel.json>
# Pruefen: Ausgabe nach tmp/ schreiben und mit cmp gegen die committete Datei
# vergleichen; byte-gleich, solange niemand die JSON-Datei von Hand aendert.
# Unsichtbare und Sonderzeichen werden per chr() erzeugt, die Datei mit
# ensure_ascii=True geschrieben, damit sie als \uXXXX sichtbar bleiben.
# Die erwarteten Werte sind aus ADR-0140 "Titel, zweite Schicht" / ADR-0044 3a
# von Hand abgeleitet, NICHT aus dem Code erzeugt.
import json, sys

ER = "Interne Karte (Titel im Board)"
V = []
FEHLT = object()  # Schalter nicht gesetzt


def b(name, quelle, titel, erwartet, vertraulich=FEHLT):
    e = {"titel": titel}
    if vertraulich is not FEHLT:
        e["vertraulich"] = vertraulich
    V.append({"name": name, "quelle": quelle, "eingabe": e, "erwartet": erwartet})


def a(name, quelle, titel, vertraulich, grund):
    V.append({"name": name, "quelle": quelle,
              "eingabe": {"titel": titel, "vertraulich": vertraulich}, "ablehnung": grund})


def c(n):
    return chr(n)


# --- normal -----------------------------------------------------------------
b("normaler Titel", "normal", "Angebot prüfen", "Angebot prüfen")
b("Umlaute und scharfes s", "normal", "Äpfel, Öl und Übermaß: Straße", "Äpfel, Öl und Übermaß: Straße")
b("Latin-1-Akzente", "normal", "Café Crème brûlée à la Ñandú", "Café Crème brûlée à la Ñandú")
b("Latin-1 Randzeichen Æ Ø þ ÿ", "normal", "Æsir Ørsted þing ÿ", "Æsir Ørsted þing ÿ")
b("Ziffern bis zwölf", "normal", "Rechnung 123456789012", "Rechnung 123456789012")
b("Datum und Telefon", "normal", "Termin 2026-10-11 Tel 12", "Termin 2026-10-11 Tel 12")
b("Domainname ohne www und ohne Schema", "normal", "Siehe example.com/pfad", "Siehe example.com/pfad")
b("Ersatztext selbst bleibt", "normal", ER, ER)

# --- trim / leer ------------------------------------------------------------
b("Leerzeichen außen werden entfernt", "trim", "   Titel   ", "Titel")
b("geschütztes Leerzeichen wird Leerzeichen und entfällt außen", "trim", c(0xA0) + "Titel" + c(0xA0), "Titel")
b("ideographisches Leerzeichen wird Leerzeichen", "trim", "A" + c(0x3000) + "B", "A B")
b("leerer Titel", "leer", "", ER)
b("nur Leerzeichen", "leer", "     ", ER)
b("nur unsichtbare Zeichen", "leer", c(0x200B) + c(0x200D) + c(0xFEFF), ER)
b("nur kyrillisch: nichts übrig", "leer", "Привет", ER)
b("nur CJK: nichts übrig", "leer", "日本語のタイトル", ER)
b("nur Emoji: nichts übrig", "leer", "\U0001F680\U0001F389", ER)
b("Emoji-Familie mit ZWJ", "leer", "\U0001F468" + c(0x200D) + "\U0001F469" + c(0x200D) + "\U0001F467", ER)

# --- NFKC -------------------------------------------------------------------
b("Vollbreite Buchstaben", "NFKC", "ＡＢＣ Ｔｉｔｅｌ", "ABC Titel")
b("Vollbreite Ziffern und Klammern", "NFKC", "（Test） １２３", "(Test) 123")
b("Vollbreites kaufmännisches Und", "NFKC", "Ｒ＆Ｄ", "R&D")
b("Ligatur fi", "NFKC", "ﬁle", "file")
b("Ligatur ffi und fl", "NFKC", "ﬃce ﬂow", "ffice flow")
b("hochgestellte Ziffer", "NFKC", "m" + c(0xB2), "m2")
b("tiefgestellte Ziffer", "NFKC", "H" + c(0x2082) + "O", "H2O")
b("eingekreiste Ziffer", "NFKC", c(0x2461) + " Schritt", "2 Schritt")
b("römische Ziffer als ein Zeichen", "NFKC", "Kapitel " + c(0x216B), "Kapitel XII")
b("Auslassungszeichen wird drei Punkte", "NFKC", "Wird fortgesetzt" + c(0x2026), "Wird fortgesetzt...")
b("doppeltes Ausrufezeichen als ein Zeichen", "NFKC", "Achtung" + c(0x203C), "Achtung!!")
b("Fragezeichen-Ausrufezeichen als ein Zeichen", "NFKC", "Echt" + c(0x2049), "Echt!?")
b("Nummernzeichen wird No", "NFKC", c(0x2116) + " 5", "No 5")
b("Trademark wird TM", "NFKC", "Pro" + c(0x2122), "ProTM")
b("Grad Celsius: Gradzeichen fällt weg", "NFKC", "20" + c(0x2103), "20C")
b("Mikro-Zeichen wird Griechisch und fällt weg", "NFKC", "5" + c(0xB5) + "m", "5m")
b("ordinales a wird a", "NFKC", "1" + c(0xAA), "1a")
b("Bruch ½ wird 1, Bruchstrich, 2; der Bruchstrich fällt weg", "NFKC", c(0xBD) + " Liter", "12 Liter")
b("zerlegtes ä wird zusammengesetzt", "NFKC", "A" + c(0x308) + "pfel", "Äpfel")
b("zerlegtes é wird zusammengesetzt", "NFKC", "Cafe" + c(0x301), "Café")
b("geschützter Bindestrich U+2011 fällt weg", "NFKC", "A" + c(0x2011) + "B", "AB")
b("Minuszeichen U+2212 fällt weg", "NFKC", "A" + c(0x2212) + "B", "AB")
b("Vollbreites at ergibt Muster verpackter Daten", "NFKC", "mail" + c(0xFF20) + "firma", ER)
b("Vollbreites www. ergibt Muster verpackter Daten", "NFKC", "ｗｗｗ．firma", ER)
b("Vollbreiter Doppelpunkt-Schrägstrich ergibt ://", "NFKC", "http" + c(0xFF1A) + c(0xFF0F) + c(0xFF0F) + "x", ER)
b("dreizehn eingekreiste Ziffern ergeben mehr als zwölf Ziffern", "NFKC",
  "".join(c(0x2460 + i) for i in range(9)) + c(0x2460) * 4, ER)

# --- unsichtbar -------------------------------------------------------------
for cp, n in [(0x200B, "ZWSP"), (0x200C, "ZWNJ"), (0x200D, "ZWJ"), (0x200E, "LRM"), (0x200F, "RLM"),
              (0x2060, "Word Joiner"), (0xFEFF, "BOM"), (0xAD, "Soft Hyphen"), (0x202A, "LRE"),
              (0x202B, "RLE"), (0x202C, "PDF"), (0x202D, "LRO"), (0x202E, "RLO"), (0x2066, "LRI"),
              (0x2067, "RLI"), (0x2068, "FSI"), (0x2069, "PDI"), (0x61C, "Arabic Letter Mark")]:
    b(n + " mitten im Wort", "unsichtbar", "Ab" + c(cp) + "cd", "Abcd")
b("Bidi-Umkehr RLO um Text", "unsichtbar", c(0x202E) + "Rechnung" + c(0x202C), "Rechnung")
b("unsichtbare Zeichen zwischen w und w. ergeben www.", "unsichtbar", "w" + c(0x200B) + "w" + c(0x200D) + "w.firma", ER)
b("Soft Hyphen im at-Muster", "unsichtbar", "a" + c(0xAD) + "@" + c(0x200B) + "b", ER)
b("ZWSP zwischen Leerzeichen und Wort", "unsichtbar", "Ein" + c(0x200B) + " Titel", "Ein Titel")
b("Cf-Zeichen verhindert Zusammensetzung (Soft Hyphen vor Umlaut-Trema)", "unsichtbar",
  "A" + c(0xAD) + c(0x308) + "pfel", "Äpfel")
b("CGJ (Mn, kein Cf) trennt das Muster erst vor dem Filtern: zweite Musterprüfung", "unsichtbar",
  "ww" + c(0x34F) + "w.firma", ER)
b("Cf-Zeichen verlängern die Lauflänge der ersten Musterprüfung (Soft Hyphen im langen Wort)", "unsichtbar",
  "Donaudampf" + c(0xAD) + "schiff" + c(0xAD) + "fahrts" + c(0xAD) + " Ende", "Donaudampfschifffahrts Ende")
b("Tag-Zeichen U+E0041 fällt weg", "unsichtbar", "A" + c(0xE0041) + "B", "AB")

# --- zeichenvorrat ----------------------------------------------------------
b("kyrillisch gemischt", "zeichenvorrat", "Привет Welt", "Welt")
b("kyrillisch im Wort", "zeichenvorrat", "Pr" + "и" + "ma", "Prma")
b("griechisch", "zeichenvorrat", "Alpha Αλφα", "Alpha")
b("CJK gemischt", "zeichenvorrat", "Test 日本語", "Test")
b("arabisch", "zeichenvorrat", "Titel مرحبا", "Titel")
b("Emoji am Ende", "zeichenvorrat", "Release \U0001F680", "Release")
b("Emoji am Anfang", "zeichenvorrat", c(0x2705) + " Erledigt", "Erledigt")
b("Emoji mit Variantenselektor", "zeichenvorrat", "Warnung " + c(0x26A0) + c(0xFE0F), "Warnung")
b("Latin Extended-A ist nicht im Vorrat", "zeichenvorrat", "Łódź", "ód")
b("Latin Extended-A Ž Č", "zeichenvorrat", "Žluťoučký Čech", "luouký ech")
b("Malzeichen und Geteilt-Zeichen", "zeichenvorrat", "3 " + c(0xD7) + " 4 " + c(0xF7) + " 2", "3 4 2")
b("Copyright-Zeichen", "zeichenvorrat", "A " + c(0xA9) + " B", "A B")
b("Paragraphenzeichen", "zeichenvorrat", c(0xA7) + " 5", "5")
b("umgekehrtes Fragezeichen", "zeichenvorrat", c(0xBF) + "Qué?", "Qué?")
b("Tabulator wird entfernt", "zeichenvorrat", "A\tB", "AB")
b("Zeilenumbruch wird entfernt", "zeichenvorrat", "A\nB", "AB")
b("Steuerzeichen NUL wird entfernt", "zeichenvorrat", "A" + c(0) + "B", "AB")
b("Eszett-Großbuchstabe U+1E9E ist nicht im Vorrat", "zeichenvorrat", "STRA" + c(0x1E9E) + "E", "STRAE")

b("Roboter-Emoji am Anfang fällt weg, die Marke steht nie im Titel", "zeichenvorrat", "\U0001F916 Titel", "Titel")
b("C1-Steuerzeichen U+0085 fällt weg", "zeichenvorrat", "A" + c(0x85) + "B", "AB")
b("C1-Steuerzeichen U+0080 fällt weg", "zeichenvorrat", "A" + c(0x80) + "B", "AB")
b("DEL fällt weg", "zeichenvorrat", "A" + c(0x7F) + "B", "AB")
b("Wagenrücklauf fällt weg", "zeichenvorrat", "A\rB", "AB")
b("nur Wagenrücklauf: nichts übrig", "zeichenvorrat", "\r", ER)
b("Zeilentrenner U+2028 fällt weg", "zeichenvorrat", "A" + c(0x2028) + "B", "AB")
b("Absatztrenner U+2029 fällt weg", "zeichenvorrat", "A" + c(0x2029) + "B", "AB")
b("mathematische Buchstaben werden durch NFKC lateinisch", "NFKC", c(0x1D400) + c(0x1D401) + c(0x1D402), "ABC")
b("kombinierendes Zeichen ohne Präkomposit fällt weg", "zeichenvorrat", "Q" + c(0x308) + "Q", "QQ")
b("kleines At-Zeichen U+FE6B wird durch NFKC zu At", "NFKC", "a" + c(0xFE6B) + "b", ER)
b("Latin-1 Ð Þ ß þ Æ sind im Vorrat", "zeichenvorrat", "Ð Þ ß þ Æ", "Ð Þ ß þ Æ")
b("hochgestellte 2 und 3 werden durch NFKC Ziffern", "NFKC", "x" + c(0xB2) + c(0xB3), "x23")
b("Ziffer ist Unicode Nd: dreizehn arabisch-indische Ziffern zählen", "verpackt",
  "".join(c(0x660 + i % 10) for i in range(13)) + " Titel", ER)
b("zwölf arabisch-indische Ziffern zählen als Ziffern, fallen dann weg", "verpackt",
  "".join(c(0x660 + i % 10) for i in range(12)) + " Titel", "Titel")
b("dreizehn äthiopische Ziffern sind keine Nd (Kategorie No) und fallen weg", "verpackt",
  "".join(c(0x1369 + i) for i in range(13)) + " Titel", "Titel")
b("einzelnes Surrogat gilt als U+FFFD und fällt weg", "unsichtbar", "A" + "\ud800" + "B", "AB")

b("Tabulator trennt Wörter der ersten Musterprüfung (Go unicode.IsSpace)", "verpackt",
  "Ж" * 12 + "\t" + "Ж" * 13 + " X", "X")
b("U+001C trennt Wörter NICHT (Python isspace ja, Go nein): Lauf über 24", "verpackt",
  "Ж" * 12 + c(0x1C) + "Ж" * 13 + " X", ER)

# --- 3a-Satzzeichen ---------------------------------------------------------
b("alle erlaubten Satzzeichen", "3a-Satzzeichen", "A. B, C: D; E - F – G / H & I (J) K + L ' M ! N ?",
  "A. B, C: D; E - F – G / H & I (J) K + L ' M ! N ?")
b("Gedankenstrich (Halbgeviert) bleibt", "3a-Satzzeichen", "Plan – Teil 2", "Plan – Teil 2")
b("langer Gedankenstrich fällt weg", "3a-Satzzeichen", "Plan " + c(0x2014) + " Teil 2", "Plan Teil 2")
b("Raute fällt weg", "3a-Satzzeichen", "Karte #809", "Karte 809")
b("Anführungszeichen fallen weg", "3a-Satzzeichen", 'Er sagte "Hallo"', "Er sagte Hallo")
b("typografische Anführungszeichen fallen weg", "3a-Satzzeichen", c(0x201E) + "Hallo" + c(0x201C), "Hallo")
b("typografischer Apostroph fällt weg", "3a-Satzzeichen", "Andi" + c(0x2019) + "s Plan", "Andis Plan")
b("gerader Apostroph bleibt", "3a-Satzzeichen", "Andi's Plan", "Andi's Plan")
b("Prozent fällt weg", "3a-Satzzeichen", "100% Abdeckung", "100 Abdeckung")
b("Unterstrich fällt weg", "3a-Satzzeichen", "a_b c_d", "ab cd")
b("Gleichheitszeichen und Sternchen fallen weg", "3a-Satzzeichen", "x = 5 * 3", "x 5 3")
b("Dollar und Euro fallen weg", "3a-Satzzeichen", "5 $ und 6 " + c(0x20AC), "5 und 6")
b("spitze und eckige Klammern fallen weg", "3a-Satzzeichen", "<a> [b] {c}", "a b c")
b("Senkrechter Strich, Tilde, Zirkumflex, Gravis fallen weg", "3a-Satzzeichen", "a|b ~c ^d `e", "ab c d e")
b("Backslash fällt weg", "3a-Satzzeichen", "a\\b", "ab")
b("Gradzeichen fällt weg", "3a-Satzzeichen", "20" + c(0xB0) + "C", "20C")
b("Mittelpunkt fällt weg", "3a-Satzzeichen", "A" + c(0xB7) + "B", "AB")
b("C++ und C-Raute", "3a-Satzzeichen", "C++ & C#", "C++ & C")

# --- verpackt ---------------------------------------------------------------
b("24 Zeichen am Stück sind erlaubt", "verpackt", "Q" * 24, "Q" * 24)
b("25 Zeichen am Stück sind verpackt", "verpackt", "Q" * 25, ER)
b("zwei Wörter mit je 24 Zeichen", "verpackt", "Q" * 24 + " " + "R" * 24, "Q" * 24 + " " + "R" * 24)
b("Base64-artiges Wort", "verpackt", "QWxhZGRpbjpvcGVuIHNlc2FtZQ", ER)
b("Base64 mit Auffüllung", "verpackt", "Q" * 24 + "==", ER)
b("langer Dateipfad ohne Leerzeichen", "verpackt", "foo.bar.baz.example.org/pfad/lang", ER)
b("zwölf Ziffern verteilt", "verpackt", "1 2 3 4 5 6 7 8 9 0 1 2", "1 2 3 4 5 6 7 8 9 0 1 2")
b("dreizehn Ziffern am Stück", "verpackt", "1234567890123", ER)
b("dreizehn Ziffern verteilt", "verpackt", "1 2 3 4 5 6 7 8 9 0 1 2 3", ER)
b("Ziffern über mehrere Wörter summiert", "verpackt", "Nr 123456 und 1234567", ER)
b("16 Hex-Zeichen am Stück sind erlaubt", "verpackt", "abcdef0123456789", "abcdef0123456789")
b("17 Hex-Zeichen am Stück sind verpackt", "verpackt", "abcdef0123456789a", ER)
b("17 Hex-Zeichen groß", "verpackt", "ABCDEF0123456789A", ER)
b("Hex-Lauf nach Doppelpunkt", "verpackt", "x:" + "deadbeef" * 3, ER)
b("zwei Hex-Läufe mit Leerzeichen", "verpackt", "deadbeef deadbeef deadbeef", "deadbeef deadbeef deadbeef")
b("Schema ://", "verpackt", "http://beispiel", ER)
b("Schema :// großgeschrieben", "verpackt", "HTTPS://Beispiel", ER)
b("www. am Wortanfang", "verpackt", "www.firma", ER)
b("www. großgeschrieben", "verpackt", "WWW.Firma", ER)
b("www. mitten im Satz", "verpackt", "Siehe www.firma bitte", ER)
b("E-Mail-Adresse", "verpackt", "a@b", ER)
b("Klammeraffe mit Leerzeichen", "verpackt", "Mail @ Büro", ER)
b("Muster entsteht erst nach dem Filtern (Tabulator trennt vorher)", "verpackt", "Q" * 13 + "\t" + "Q" * 13, ER)
b("Hex-Muster entsteht erst nach dem Filtern", "verpackt", "abcdef01\tabcdef012", ER)

# --- max80 ------------------------------------------------------------------
def wort(n):
    """Text aus 9-Zeichen-Wörtern, genau n Zeichen, letztes Zeichen ein Buchstabe."""
    s = ("Projektplan " * 20)[:n]
    assert not s.endswith(" ") and len(s) == n
    return s


b("79 Zeichen", "max80", wort(79), wort(79))
b("80 Zeichen", "max80", wort(80), wort(80))
b("81 Zeichen werden auf 80 gekürzt", "max80", wort(81), wort(80))
b("200 Zeichen werden auf 80 gekürzt", "max80", wort(200), wort(80))
# 80. Zeichen ist ein Leerzeichen: nach dem Kürzen wird erneut getrimmt (79)
s = wort(79) + " Rest"
assert s[79] == " " and len(s) == 84
b("Schnitt trifft ein Leerzeichen: danach getrimmt", "max80", s, s[:79])
b("80 Umlaute-Zeichen zählen als 80 Zeichen, nicht als Bytes", "max80", ("Ärgerliches " * 7)[:80].rstrip(), ("Ärgerliches " * 7)[:80].rstrip())
# Zählung NACH NFKC: 79 Zeichen, davon eine Ligatur (zählt 2) -> 80
t79 = wort(79)
lig = t79[:5] + c(0xFB01) + t79[5:]  # 80 Zeichen vor NFKC? nein: 79 + 1 = 80 Zeichen roh
assert len(lig) == 80
b("Ligatur: 80 Zeichen roh, 81 nach NFKC, wird auf 80 gekürzt", "max80",
  lig, (t79[:5] + "fi" + t79[5:])[:80])
lig2 = t79[:5] + c(0xFB01) + t79[6:]  # 79 Zeichen roh, 80 nach NFKC
assert len(lig2) == 79
b("Ligatur: 79 Zeichen roh, 80 nach NFKC, bleibt ganz", "max80", lig2, t79[:5] + "fi" + t79[6:])
# Unsichtbare und gefilterte Zeichen zählen nicht mit
b("unsichtbare Zeichen zählen nicht zur Länge", "max80", wort(80)[:40] + c(0x200B) * 10 + wort(80)[40:], wort(80))
b("gefilterte Emoji zählen nicht zur Länge", "max80", wort(80)[:40] + "\U0001F680" * 10 + wort(80)[40:], wort(80))
b("81 Zeichen, davon eines außerhalb des Vorrats: 80 bleiben", "max80", wort(80)[:40] + "Ж" + wort(80)[40:], wort(80))


b("Muster erst hinter Zeichen 80 wird noch erkannt (Prüfung vor dem Kürzen)", "max80", wort(80) + " www.x", ER)
b("Hex-Lauf erst hinter Zeichen 80 wird noch erkannt", "max80", wort(80) + " " + "abcdef0123456789a", ER)
b("dreizehnte Ziffer hinter Zeichen 80 wird noch gezählt", "max80", "1 2 3 4 5 6 7 8 9 0 1 2 " + "x " * 30 + "3", ER)

# --- leerraum (L-1) ---------------------------------------------------------
b("zwei Leerzeichen werden eines", "leerraum", "A  B", "A B")
b("fünf Leerzeichen werden eines", "leerraum", "A     B", "A B")
b("Leerzeichen-Läufe mehrfach", "leerraum", "A  B   C    D", "A B C D")
b("zwei geschützte Leerzeichen werden eines", "leerraum", "A" + c(0xA0) + c(0xA0) + "B", "A B")
b("geschütztes plus gewöhnliches Leerzeichen werden eines", "leerraum", "A" + c(0xA0) + " B", "A B")
b("Geviert U+2003 wird Leerzeichen", "leerraum", "A" + c(0x2003) + "B", "A B")
b("U+2000 bis U+200A werden je ein Leerzeichen und ziehen sich zusammen", "leerraum",
  "A" + "".join(c(0x2000 + i) for i in range(11)) + "B", "A B")
b("schmales geschütztes Leerzeichen U+202F wird Leerzeichen", "leerraum", "A" + c(0x202F) + "B", "A B")
b("zwei ideographische Leerzeichen werden eines", "leerraum", "A" + c(0x3000) + c(0x3000) + "B", "A B")
b("Tabulator zwischen Leerzeichen: Filter entfernt ihn, der Lauf zieht sich zusammen", "leerraum", "A \t B", "A B")
b("Zeilenumbruch zwischen Leerzeichen", "leerraum", "A \n B", "A B")
b("U+2028 zwischen Leerzeichen", "leerraum", "A " + c(0x2028) + " B", "A B")
b("Leerzeichen am Rand nach gemischtem Leerraum", "leerraum", c(0xA0) + " " + c(0x3000) + "Titel" + c(0x2003) + " ", "Titel")
b("Filter lässt Leerzeichen zusammenstoßen", "leerraum", "A \u0416 B", "A B")
b("nur Leerraum verschiedener Art ergibt den Ersatztext", "leerraum", " " + c(0xA0) + c(0x3000) + "\t", ER)
b("mittleres mathematisches Leerzeichen U+205F wird Leerzeichen", "leerraum", "A" + c(0x205F) + "B", "A B")
b("Ogham-Leerzeichen U+1680 fällt weg (Filter)", "leerraum", "A" + c(0x1680) + "B", "AB")
b("Ogham-Leerzeichen zwischen Leerzeichen: Lauf zieht sich zusammen", "leerraum", "A " + c(0x1680) + " B", "A B")
b("vertikaler Tabulator und Seitenvorschub fallen weg", "leerraum", "A \x0b\x0c B", "A B")
b("Mongolischer Vokaltrenner U+180E (Cf) fällt weg", "leerraum", "A" + c(0x180E) + "B", "AB")
b("Zählung nach dem Zusammenziehen: 83 Zeichen roh, 79 danach, bleibt ganz", "leerraum",
  "Plan" + " " * 5 + wort(74), "Plan " + wort(74))
b("Zusammenziehen vor dem Kürzen, danach erneut trimmen", "leerraum",
  wort(79) + "   Rest", wort(79))

# --- vertraulich ------------------------------------------------------------
b("Schalter fehlt: Titel bleibt", "vertraulich", "Offener Titel", "Offener Titel")
b("Schalter false: Titel bleibt", "vertraulich", "Offener Titel", "Offener Titel", False)
b("Schalter true: kein Titel", "vertraulich", "Geheimer Titel", None, True)
b("Schalter null: vertraulich", "vertraulich", "Geheimer Titel", None, None)
b("Schalter Text \"false\": vertraulich", "vertraulich", "Geheimer Titel", None, "false")
b("Schalter Text \"true\": vertraulich", "vertraulich", "Geheimer Titel", None, "true")
b("Schalter leerer Text: vertraulich", "vertraulich", "Geheimer Titel", None, "")
b("Schalter Zahl 0: vertraulich", "vertraulich", "Geheimer Titel", None, 0)
b("Schalter Zahl 1: vertraulich", "vertraulich", "Geheimer Titel", None, 1)
b("Schalter leere Liste: vertraulich", "vertraulich", "Geheimer Titel", None, [])
b("Schalter leeres Objekt: vertraulich", "vertraulich", "Geheimer Titel", None, {})
b("vertraulich mit Muster-Titel: kein Titel, kein Ersatztext", "vertraulich", "a@b", None, True)

# --- Ablehnung am Eingang ---------------------------------------------------
# eingabe = Titel und Schalter, wie das Skript sie in einem Schatten schickt;
# ablehnung = Feld, das die Ablehnung nennen muss.
a("kyrillischer Titel wurde nicht behandelt", "eingang", "Привет", False, "titel")
a("Titel mit Emoji wurde nicht behandelt", "eingang", "Release \U0001F680", False, "titel")
a("Titel mit Leerzeichen am Rand", "eingang", " Titel", False, "titel")
a("Titel mit Leerzeichen am Ende", "eingang", "Titel ", False, "titel")
a("Titel nicht NFKC-normalisiert", "eingang", "ﬁle", False, "titel")
a("Titel mit ZWSP", "eingang", "Ab" + c(0x200B) + "cd", False, "titel")
a("Titel mit Soft Hyphen", "eingang", "Ab" + c(0xAD) + "cd", False, "titel")
a("Titel mit Klammeraffe", "eingang", "a@b", False, "titel")
a("Titel mit www.", "eingang", "www.firma", False, "titel")
a("Titel mit ://", "eingang", "http://x", False, "titel")
a("Titel mit 25 Zeichen am Stück", "eingang", "Q" * 25, False, "titel")
a("Titel mit 13 Ziffern", "eingang", "1234567890123", False, "titel")
a("Titel mit 17 Hex-Zeichen", "eingang", "abcdef0123456789a", False, "titel")
a("Titel mit 81 Zeichen", "eingang", wort(81), False, "titel")
a("Titel mit Rautezeichen", "eingang", "Karte #809", False, "titel")
a("Titel mit zwei Leerzeichen", "eingang", "A  B", False, "titel")
a("Titel mit geschütztem Leerzeichen", "eingang", "A" + c(0xA0) + "B", False, "titel")
a("Titel mit Tabulator", "eingang", "A\tB", False, "titel")
a("Titel mit Zeilenumbruch", "eingang", "A\nB", False, "titel")
a("Titel mit Roboter-Marke: sie steht nie im Titel", "eingang", "\U0001F916 Titel", False, "titel")
a("Titel mit Roboter-Marke ohne Leerzeichen", "eingang", "\U0001F916Titel", False, "titel")
a("leerer Titel wurde nicht zum Ersatztext", "eingang", "", False, "titel")
a("Titel null bei vertraulich false", "eingang", None, False, "titel")
a("Titel als Zahl", "eingang", 5, False, "titel")
a("Titel als Liste", "eingang", ["x"], False, "titel")
a("Titel gesetzt bei vertraulich true", "eingang", "Geheimer Titel", True, "titel")
a("Ersatztext bei vertraulich true", "eingang", ER, True, "titel")
a("Schalter als Text \"false\"", "eingang", "Titel", "false", "vertraulich")
a("Schalter als Zahl 0", "eingang", "Titel", 0, "vertraulich")
a("Schalter null", "eingang", "Titel", None, "vertraulich")

# =============================================================================
# Felder spalte und bearbeiter (Absprache Board-Paar 11.10.2026, Ziffer 5).
# Ein Vektor prueft genau ein Feld: "feld" fehlt = titel.
# =============================================================================
def sp(name, quelle, roh, erwartet):
    V.append({"name": "Spalte: " + name, "quelle": quelle, "feld": "spalte",
              "eingabe": {"spalte": roh}, "erwartet": erwartet})


def spa(name, roh):
    V.append({"name": "Spalte (Eingang): " + name, "quelle": "eingang", "feld": "spalte",
              "eingabe": {"spalte": roh}, "ablehnung": "spalte"})


def be(name, quelle, liste, erwartet):
    V.append({"name": "Bearbeiter: " + name, "quelle": quelle, "feld": "bearbeiter",
              "eingabe": {"bearbeiter": liste}, "erwartet": erwartet})


def bea(name, liste):
    V.append({"name": "Bearbeiter (Eingang): " + name, "quelle": "eingang", "feld": "bearbeiter",
              "eingabe": {"bearbeiter": liste}, "ablehnung": "bearbeiter"})


def fuell(n):
    """Text aus 10-Buchstaben-Woertern mit Leerzeichen, genau n Zeichen, letztes ein Buchstabe."""
    s = ("Abcdefghij " * 20)[:n]
    assert len(s) == n and not s.endswith(" ")
    return s


# --- Spalte: Behandlung -----------------------------------------------------
# erwartet ist der Spaltenname, wie er in den Schatten kommt; bleibt nichts
# uebrig oder greift ein Muster, ist es der leere Text (kein Ersatztext).
sp("normaler Name", "spalte", "In Arbeit", "In Arbeit")
sp("Backlog", "spalte", "Backlog", "Backlog")
sp("Umlaute und Satzzeichen", "spalte", "Ärger & Öl: (neu)!", "Ärger & Öl: (neu)!")
sp("leerer Name bleibt leer", "spalte", "", "")
sp("nur Leerzeichen ergibt leer", "spalte", "     ", "")
sp("Leerzeichen-Lauf wird eines", "spalte", "Zu    prüfen", "Zu prüfen")
sp("mehrere Läufe", "spalte", "A  B   C", "A B C")
sp("Rand-Leerzeichen", "spalte", "   Ready   ", "Ready")
sp("geschütztes Leerzeichen am Rand und in der Mitte", "spalte", c(0xA0) + "Zu" + c(0xA0) + c(0xA0) + "tun" + c(0xA0), "Zu tun")
sp("ideographisches Leerzeichen", "spalte", "Zu" + c(0x3000) + "tun", "Zu tun")
sp("Tabulator fällt weg", "spalte", "Zu" + c(9) + "tun", "Zutun")
sp("Tabulator zwischen Leerzeichen", "spalte", "Zu " + c(9) + " tun", "Zu tun")
sp("Zeilenumbruch fällt weg", "spalte", "Zu" + c(10) + "tun", "Zutun")
sp("ZWSP fällt weg", "spalte", "Er" + c(0x200B) + "ledigt", "Erledigt")
sp("Soft Hyphen fällt weg", "spalte", "Er" + c(0xAD) + "ledigt", "Erledigt")
sp("BOM und Bidi-Zeichen fallen weg", "spalte", c(0xFEFF) + c(0x202E) + "Review" + c(0x202C), "Review")
sp("nur unsichtbare Zeichen ergibt leer", "spalte", c(0x200B) + c(0xFEFF), "")
sp("NFKC: vollbreite Buchstaben", "spalte", "Ｂａｃｋｌｏｇ", "Backlog")
sp("NFKC: Ligatur", "spalte", "ﬁnal", "final")
sp("NFKC: zerlegtes ä", "spalte", "A" + c(0x308) + "rger", "Ärger")
sp("kyrillisch ergibt leer", "spalte", "Привет", "")
sp("kyrillisch gemischt", "spalte", "Доска Board", "Board")
sp("CJK ergibt leer", "spalte", "日本語", "")
sp("arabisch gemischt", "spalte", "Spalte مرحبا", "Spalte")
sp("Emoji am Ende", "spalte", "Fertig " + c(0x1F680), "Fertig")
sp("Emoji ergibt leer", "spalte", c(0x1F680) + c(0x1F389), "")
sp("Raute fällt weg", "spalte", "Phase #2", "Phase 2")
sp("Anführungszeichen fallen weg", "spalte", '"Doing"', "Doing")
sp("Unterstrich fällt weg", "spalte", "to_do", "todo")
sp("39 Zeichen", "max40", fuell(39), fuell(39))
sp("40 Zeichen", "max40", fuell(40), fuell(40))
sp("41 Zeichen werden auf 40 gekürzt", "max40", fuell(41), fuell(40))
sp("100 Zeichen werden auf 40 gekürzt", "max40", fuell(100), fuell(40))
s = "Abcdefghij " * 3 + "Abcdef" + " Rest"
assert s[39] == " " and len(s) == 44
sp("Schnitt trifft ein Leerzeichen: danach getrimmt", "max40", s, s[:39])
sp("Zählung nach dem Zusammenziehen: 44 Zeichen roh, 40 danach, bleibt ganz", "max40",
   "Abcdefghij " * 3 + "Abcde" + " " * 5 + "x", "Abcdefghij " * 3 + "Abcde x")
sp("Zählung nach NFKC: Ligatur macht aus 40 Zeichen 41", "max40",
   fuell(40)[:5] + c(0xFB01) + fuell(40)[5:], (fuell(40)[:5] + "fi" + fuell(40)[5:])[:40])
sp("Muster hinter Zeichen 40 wird noch erkannt", "max40", fuell(40) + " www.x", "")
sp("Muster: www.", "spalte", "www.firma", "")
sp("Muster: Klammeraffe", "spalte", "a@b", "")
sp("Muster: ://", "spalte", "http://x", "")
sp("Muster: 25 Zeichen am Stück", "spalte", "Q" * 25, "")
sp("24 Zeichen am Stück sind erlaubt", "spalte", "Q" * 24, "Q" * 24)
sp("Muster: dreizehn Ziffern", "spalte", "1234567890123", "")
sp("zwölf Ziffern sind erlaubt", "spalte", "123456789012", "123456789012")
sp("Muster: 17 Hex-Zeichen", "spalte", "abcdef0123456789a", "")
sp("16 Hex-Zeichen sind erlaubt", "spalte", "abcdef0123456789", "abcdef0123456789")
sp("Muster entsteht erst nach dem Filtern", "spalte", "Q" * 13 + c(9) + "Q" * 13, "")
sp("Muster durch vollbreites at", "spalte", "mail" + c(0xFF20) + "firma", "")
sp("Muster durch unsichtbare Zeichen: w ZWSP w ZWJ w.", "spalte", "w" + c(0x200B) + "w" + c(0x200D) + "w.firma", "")

# --- Spalte: Eingang ---------------------------------------------------------
spa("kyrillisch wurde nicht behandelt", "Привет")
spa("Emoji wurde nicht behandelt", "Fertig " + c(0x1F680))
spa("Leerzeichen am Anfang", " Backlog")
spa("Leerzeichen am Ende", "Backlog ")
spa("zwei Leerzeichen", "A  B")
spa("geschütztes Leerzeichen", "A" + c(0xA0) + "B")
spa("ideographisches Leerzeichen", "A" + c(0x3000) + "B")
spa("Tabulator", "A" + c(9) + "B")
spa("ZWSP", "Er" + c(0x200B) + "ledigt")
spa("Soft Hyphen", "Er" + c(0xAD) + "ledigt")
spa("nicht NFKC-normalisiert", "ﬁnal")
spa("41 Zeichen", fuell(41))
spa("100 Zeichen", fuell(100))
spa("Klammeraffe", "a@b")
spa("www.", "www.firma")
spa("://", "http://x")
spa("25 Zeichen am Stück", "Q" * 25)
spa("dreizehn Ziffern", "1234567890123")
spa("17 Hex-Zeichen", "abcdef0123456789a")
spa("Raute", "Phase #2")
spa("null statt Text", None)
spa("Zahl statt Text", 5)
spa("Liste statt Text", ["Backlog"])

# --- Bearbeiter: Behandlung ---------------------------------------------------
# erwartet ist die Liste, wie der Ausgang sie in den Schatten schreibt, wenn
# JEDER Name der Eingabe dem Board bekannt ist (die Zugehoerigkeit zu
# board.identities bzw. den Claude-Modellen ist Sache des Ausgangs und nicht
# Teil dieser Vektoren). Ein Name, der die Behandlung nicht uebersteht, faellt
# aus der Liste weg (er wird nie gekuerzt oder ersetzt).
be("normaler Name", "bearbeiter", ["Anna Berg"], ["Anna Berg"])
be("zwei Namen, Reihenfolge bleibt", "bearbeiter", ["Zoe Weiss", "Anna Berg"], ["Zoe Weiss", "Anna Berg"])
be("Claude-Modell", "bearbeiter", ["Claude Opus"], ["Claude Opus"])
be("leere Liste", "bearbeiter", [], [])
be("Liste mit leerem Eintrag", "bearbeiter", ["", "Anna Berg"], ["Anna Berg"])
be("Liste nur mit leeren Einträgen", "bearbeiter", ["", "   "], [])
be("Leerzeichen-Lauf wird eines", "bearbeiter", ["Anna    Berg"], ["Anna Berg"])
be("Rand-Leerzeichen", "bearbeiter", ["   Anna Berg   "], ["Anna Berg"])
be("geschütztes Leerzeichen", "bearbeiter", ["Anna" + c(0xA0) + c(0xA0) + "Berg"], ["Anna Berg"])
be("ideographisches Leerzeichen", "bearbeiter", ["Anna" + c(0x3000) + "Berg"], ["Anna Berg"])
be("Geviert U+2003", "bearbeiter", ["Anna" + c(0x2003) + "Berg"], ["Anna Berg"])
be("Tabulator fällt weg", "bearbeiter", ["Anna" + c(9) + "Berg"], ["AnnaBerg"])
be("ZWSP fällt weg", "bearbeiter", ["An" + c(0x200B) + "na"], ["Anna"])
be("Soft Hyphen fällt weg", "bearbeiter", ["An" + c(0xAD) + "na"], ["Anna"])
be("Bidi-Umkehr fällt weg", "bearbeiter", [c(0x202E) + "Anna" + c(0x202C)], ["Anna"])
be("NFKC: vollbreite Buchstaben", "bearbeiter", ["Ａｎｎａ Ｂｅｒｇ"], ["Anna Berg"])
be("NFKC: zerlegtes ä", "bearbeiter", ["J" + "a" + c(0x308) + "ger"], ["Jäger"])
be("kyrillisch ergibt: Name fällt weg", "bearbeiter", ["Иван"], [])
be("kyrillisch gemischt", "bearbeiter", ["Иван Ivan"], ["Ivan"])
be("CJK: Name fällt weg", "bearbeiter", ["日本語"], [])
be("Emoji am Ende", "bearbeiter", ["Anna " + c(0x1F680)], ["Anna"])
be("nur Emoji: Name fällt weg", "bearbeiter", [c(0x1F680)], [])
be("Latin-1-Akzente", "bearbeiter", ["José Ñandú"], ["José Ñandú"])
be("59 Zeichen", "max60", [fuell(59)], [fuell(59)])
be("60 Zeichen", "max60", [fuell(60)], [fuell(60)])
be("61 Zeichen werden auf 60 gekürzt", "max60", [fuell(61)], [fuell(60)])
be("200 Zeichen werden auf 60 gekürzt", "max60", [fuell(200)], [fuell(60)])
s = "Abcdefghij " * 5 + "Abcd" + " Rest"
assert s[59] == " " and len(s) == 64
be("Schnitt trifft ein Leerzeichen: danach getrimmt", "max60", [s], [s[:59]])
be("Muster hinter Zeichen 60 wird noch erkannt: Name fällt weg", "max60", [fuell(60) + " www.x", "Anna Berg"], ["Anna Berg"])
be("Klammeraffe: Name fällt weg", "bearbeiter", ["anna@berg.de"], [])
be("Klammeraffe neben gültigem Namen", "bearbeiter", ["Anna Berg", "a@b", "Zoe Weiss"], ["Anna Berg", "Zoe Weiss"])
be("vollbreites at (NFKC) fällt weg", "bearbeiter", ["Anna" + c(0xFF20) + "Berg"], [])
be("kleines At-Zeichen U+FE6B fällt weg", "bearbeiter", ["Anna" + c(0xFE6B) + "Berg"], [])
be("external: fällt weg", "bearbeiter", ["external:bob"], [])
be("External: großgeschrieben fällt weg", "bearbeiter", ["External:Bob"], [])
be("EXTERNAL: fällt weg", "bearbeiter", ["EXTERNAL:Bob"], [])
be("external: mit Leerzeichen davor fällt weg", "bearbeiter", ["  external:bob"], [])
be("external: mit geschütztem Leerzeichen davor fällt weg", "bearbeiter", [c(0xA0) + "external:bob"], [])
be("external: erst nach dem Säubern (Soft Hyphen) fällt weg", "bearbeiter", ["ex" + c(0xAD) + "ternal:Bob"], [])
be("external: erst nach dem Säubern (ZWSP, vollbreit) fällt weg", "bearbeiter", ["ex" + c(0x200B) + "ｔｅｒｎａｌ:Bob"], [])
be("external ohne Doppelpunkt bleibt", "bearbeiter", ["external Bob"], ["external Bob"])
be("Name enthält external: nicht am Anfang", "bearbeiter", ["Bob external:x"], ["Bob external:x"])
be("Muster: 25 Zeichen am Stück", "bearbeiter", ["Q" * 25], [])
be("24 Zeichen am Stück sind erlaubt", "bearbeiter", ["Q" * 24], ["Q" * 24])
be("Muster: dreizehn Ziffern", "bearbeiter", ["1234567890123"], [])
be("Muster: 17 Hex-Zeichen", "bearbeiter", ["abcdef0123456789a"], [])
be("Muster: www.", "bearbeiter", ["www.firma"], [])
be("Muster: ://", "bearbeiter", ["http://x"], [])
be("Muster entsteht erst nach dem Filtern", "bearbeiter", ["Q" * 13 + c(9) + "Q" * 13], [])
be("Raute fällt weg", "bearbeiter", ["Team #2"], ["Team 2"])
be("zehn Namen bleiben", "bearbeiter", ["Person %d" % i for i in range(10)], ["Person %d" % i for i in range(10)])
be("elf Namen: die ersten zehn", "bearbeiter", ["Person %d" % i for i in range(11)], ["Person %d" % i for i in range(10)])
be("ungültige Namen zählen nicht gegen die Grenze zehn", "bearbeiter",
   ["a@b"] + ["Person %d" % i for i in range(10)], ["Person %d" % i for i in range(10)])

# Doppelte Namen (Absprache Board-Paar 11.10.2026): Das Push-Skript normalisiert jeden
# Eintrag zuerst und entfernt danach Doppelte (erstes Vorkommen bleibt, Reihenfolge
# bleibt). Gemessen am Go-Ausgang (filtere): Doppel werden NACH dem Saeubern
# entfernt und zaehlen nicht gegen die Grenze zehn.
be("exaktes Doppel: nur das erste bleibt", "bearbeiter", ["Anna Berg", "Anna Berg"], ["Anna Berg"])
be("Doppel erst nach dem Säubern gleich (Leerzeichen-Lauf)", "bearbeiter", ["Anna Berg", "Anna    Berg"], ["Anna Berg"])
be("Doppel erst nach dem Säubern gleich (Rand-Leerzeichen)", "bearbeiter", ["Anna Berg", "  Anna Berg  "], ["Anna Berg"])
be("Doppel erst nach dem Säubern gleich (geschütztes Leerzeichen)", "bearbeiter", ["Anna Berg", "Anna" + c(0xA0) + "Berg"], ["Anna Berg"])
be("Doppel erst nach dem Säubern gleich (NFKC vollbreit)", "bearbeiter", ["Anna Berg", "Ａｎｎａ Ｂｅｒｇ"], ["Anna Berg"])
be("Doppel erst nach dem Säubern gleich (Soft Hyphen)", "bearbeiter", ["Anna", "An" + c(0xAD) + "na"], ["Anna"])
be("Doppel erst nach dem Säubern gleich (ZWSP)", "bearbeiter", ["Anna", "An" + c(0x200B) + "na"], ["Anna"])
be("Doppel: die rohe Form des ersten Vorkommens ist egal, es bleibt die behandelte", "bearbeiter", ["An" + c(0xAD) + "na", "Anna"], ["Anna"])
be("Doppel in der Mitte: Reihenfolge des ersten Vorkommens", "bearbeiter", ["Anna Berg", "Zoe Weiss", "Zoe  Weiss"], ["Anna Berg", "Zoe Weiss"])
be("Doppel am Ende: Reihenfolge des ersten Vorkommens", "bearbeiter", ["Zoe Weiss", "Anna Berg", "Zoe Weiss"], ["Zoe Weiss", "Anna Berg"])
be("Doppel am Anfang wiederholt: Reihenfolge des ersten Vorkommens", "bearbeiter", ["Anna Berg", "Anna Berg", "Zoe Weiss", "Anna Berg"], ["Anna Berg", "Zoe Weiss"])
be("Doppel nach dem Kürzen auf 60 Zeichen", "max60", [fuell(61), fuell(60)], [fuell(60)])
be("Doppel zählen nicht gegen die Grenze zehn", "bearbeiter",
   ["Person %d" % i for i in range(10)] + ["Person 0"] + ["Person 10"], ["Person %d" % i for i in range(10)])
be("elf Einträge mit einem Doppel: zehn verschiedene bleiben", "bearbeiter",
   ["Person 0", "Person 0"] + ["Person %d" % i for i in range(1, 10)], ["Person %d" % i for i in range(10)])
be("Doppel nach dem Säubern zählen nicht gegen die Grenze zehn", "bearbeiter",
   ["Person 0", "Person" + c(0xA0) + "0"] + ["Person %d" % i for i in range(1, 10)], ["Person %d" % i for i in range(10)])

# --- Bearbeiter: Eingang -------------------------------------------------------
bea("normale Liste mit einem unbehandelten Namen (kyrillisch)", ["Anna Berg", "Иван"])
bea("kyrillischer Name", ["Иван"])
bea("Emoji im Namen", ["Anna " + c(0x1F680)])
bea("Leerzeichen am Anfang", [" Anna Berg"])
bea("Leerzeichen am Ende", ["Anna Berg "])
bea("zwei Leerzeichen", ["Anna  Berg"])
bea("geschütztes Leerzeichen", ["Anna" + c(0xA0) + "Berg"])
bea("ideographisches Leerzeichen", ["Anna" + c(0x3000) + "Berg"])
bea("Tabulator", ["Anna" + c(9) + "Berg"])
bea("ZWSP", ["An" + c(0x200B) + "na"])
bea("Soft Hyphen", ["An" + c(0xAD) + "na"])
bea("nicht NFKC-normalisiert", ["Ａｎｎａ"])
bea("61 Zeichen", [fuell(61)])
bea("Klammeraffe", ["anna@berg.de"])
bea("external:", ["external:bob"])
bea("External: großgeschrieben", ["External:Bob"])
bea("external: mit Leerzeichen davor", [" external:bob"])
bea("external: mit Soft Hyphen", ["ex" + c(0xAD) + "ternal:Bob"])
bea("www.", ["www.firma"])
bea("25 Zeichen am Stück", ["Q" * 25])
bea("dreizehn Ziffern", ["1234567890123"])
bea("17 Hex-Zeichen", ["abcdef0123456789a"])
bea("leerer Eintrag", [""])
bea("Eintrag nur Leerzeichen", ["   "])
bea("leerer Eintrag neben gültigem", ["Anna Berg", ""])
bea("Eintrag null", [None])
bea("Eintrag Zahl", [5])
bea("Eintrag Liste", [["Anna Berg"]])
bea("elf Einträge", ["Person %d" % i for i in range(11)])
bea("Liste null", None)
bea("Text statt Liste", "Anna Berg")
bea("Objekt statt Liste", {"a": "Anna Berg"})

kopf = {
    "herkunft": {"repo": "aBoard", "commit": "Entwurf, wird beim Uebernehmen durch das Partnerprojekt gesetzt"},
    "stand": "Entwurf aBoard 2026-10-11 (Karte #809, F-2017) - noch nicht vom Partnerprojekt abgenommen",
    "hinweis": (
        "Gemeinsame Testvektoren der Titelbehandlung (T16, ADR-0140 'Titel, zweite Schicht'). "
        "Zwei Arten von Vektoren, unterschieden am Schluessel. "
        "(1) 'erwartet': Behandlung. eingabe.titel ist der ROHE Titel einer Karte, eingabe.vertraulich "
        "der Wert des Vertraulich-Schalters der Karte (im Board: 'confidential'); fehlt der Schluessel, ist der "
        "Schalter nicht gesetzt. Vertraulich gilt fail-closed: nur 'nicht gesetzt' und das echte false sind "
        "nicht vertraulich, jeder andere Wert (true, null, Text, Zahl, Liste, Objekt) ist vertraulich. "
        "erwartet ist der Titel, der in den Schatten kommt, oder null (vertraulich). Reihenfolge: unsichtbare "
        "Zeichen (Unicode Cf) entfernen, NFKC, Muster verpackter Daten pruefen, auf den Zeichenvorrat "
        "filtern, Muster erneut pruefen, jeden Lauf von Leerzeichen zu EINEM zusammenziehen und die Raender "
        "trimmen, auf 80 Zeichen (Unicode-Zeichen, gezaehlt nach NFKC, Filter und Zusammenziehen) kuerzen und "
        "erneut trimmen. Die Musterpruefung liegt VOR dem Kuerzen (ein Muster hinter Zeichen 80 zaehlt). "
        "Nach NFKC und Filter ist das gewoehnliche Leerzeichen U+0020 der einzige Leerraum: NBSP, U+2000-U+200A, "
        "U+202F, U+205F und U+3000 macht NFKC zu U+0020; Tab, LF, VT, FF, CR, U+0085, U+1680, U+2028 und U+2029 "
        "entfernt der Filter; U+180E ist Cf und faellt schon vorher weg. "
        "In der ERSTEN Musterpruefung (Wort ohne Leerzeichen laenger als 24 Zeichen) trennt ein Wort, was Go "
        "unicode.IsSpace ist, gemessen nach Cf-Entfernung und NFKC: U+0009-U+000D, U+0020, U+0085, U+00A0, U+1680, "
        "U+2000-U+200A, U+2028, U+2029, U+202F, U+205F, U+3000 - und ausdruecklich NICHT U+001C-U+001F (Python "
        "isspace() und re \\s zaehlen die als Leerraum, Go nicht; sie trennen kein Wort). Im Ergebnis stehen deshalb nie zwei Leerzeichen hintereinander und keines am Rand. "
        "Ersatztext 'Interne Karte (Titel im Board)'. Zeichenvorrat: A-Z, a-z, Latin-1-Buchstaben "
        "U+00C0-U+00FF ausser U+00D7 und U+00F7, Ziffern, Leerzeichen und . , : ; - (Halbgeviert) / & ( ) + ' ! ? "
        "Muster: Wort ohne Leerzeichen laenger als 24 Zeichen, mehr als 12 Ziffern insgesamt, '://', 'www.' "
        "(ohne Gross/Klein), '@', mehr als 16 Hex-Zeichen am Stueck. "
        "(2) 'ablehnung': Eingang. eingabe.titel und eingabe.vertraulich stehen so in einem Schatten, den das "
        "Skript schickt (titel = bereits behandelter Titel oder null, vertraulich = JSON-Wert); der Eingang "
        "muss die ganze Liste ablehnen, und die Fehlermeldung nennt das Feld aus 'ablehnung'. "
        "Jeder 'erwartet'-Titel muss zusaetzlich vom Eingang unveraendert angenommen werden (Fixpunkt), und jeder "
        "ROHE Titel, der sich von 'erwartet' unterscheidet (nicht vertraulich), muss vom Eingang mit 422 am Feld titel "
        "abgelehnt werden (der Eingang saeubert nie). "
        "Auslegung: Ziffer ist Unicode Nd (Go unicode.IsDigit; Python str.isdecimal() bzw. re \\d ohne ASCII-Flag, NICHT "
        "isdigit()). Latin-1-Buchstaben sind U+00C0-U+00FF ausser U+00D7 und U+00F7 (Ae, Eth, Thorn, sz, thorn "
        "eingeschlossen). Die Roboter-Marke (U+1F916) laeuft ueber das Feld vonAgent und steht NIE im Titel; ein "
        "Titel mit Marke wird vom Eingang abgelehnt. Ein einzelnes Surrogat (JSON \\ud800 ohne Gegenstueck) gilt "
        "als U+FFFD und faellt wie jedes Zeichen ausserhalb des Vorrats weg. "
        "Die Reihenfolge Zusammenziehen vor Kuerzen ist festgelegt: 80 zaehlt den zusammengezogenen Text. "
        "'quelle' nennt die Regel, die der Vektor belegt. "
        "FELDER (Absprache Board-Paar 11.10.2026, Ziffer 5): Ein Vektor prueft genau ein Feld. Der optionale Schluessel "
        "'feld' ('titel' | 'spalte' | 'bearbeiter') sagt welches; fehlt er, ist es 'titel' (alle Vektoren oben). "
        "feld 'spalte': eingabe.spalte ist der ROHE Spaltenname (Ausgang: Titel einer Spalte aus columnDefs); "
        "'erwartet' ist der Name, wie er in den Schatten kommt: dieselbe Behandlung wie der Titel (gleicher "
        "Leerraum, gleiche Musterpruefung vor dem Kuerzen), aber auf hoechstens 40 Zeichen. Bleibt nichts uebrig "
        "oder greift ein Muster, ist der Wert der LEERE Text (nie der Ersatztext, die Spalte bleibt leer). Der "
        "leere Text ist am Eingang erlaubt. 'ablehnung' ist 'spalte'. Der Eingang nimmt nur den leeren Text oder "
        "einen Fixpunkt der Behandlung (hoechstens 40) an. "
        "feld 'bearbeiter': eingabe.bearbeiter ist die ROHE Liste der Namen (Karte: owner und assignees, Termin: "
        "personen); 'erwartet' ist die Liste, die der Ausgang in den Schatten schreibt, wenn JEDER Name der Eingabe "
        "dem Board bekannt ist (ob ein Name in board.identities oder bei den Claude-Modellen steht, ist Sache des "
        "Ausgangs und nicht Teil dieser Vektoren). Je Name: trimmen; leere Namen entfallen; Namen mit '@' oder dem "
        "Anfang 'external:' (ohne Gross/Klein, nach dem Trimmen) entfallen; dann dieselbe Behandlung wie der Titel mit "
        "hoechstens 60 Zeichen (Ueberlaenge wird GEKUERZT, nicht abgelehnt); greift ein Muster oder bleibt nichts "
        "uebrig, ENTFAELLT der Name (er wird nie ersetzt); ist der behandelte Name 'external:'-/'@'-haltig (z. B. "
        "Soft Hyphen im Wort external, vollbreites at), entfaellt er ebenfalls. Die Reihenfolge bleibt; es gelten "
        "hoechstens 10 Namen (die ersten 10 gueltigen). Doppelte Namen nach der Behandlung faellt der Ausgang zu "
        "einem zusammen: das ERSTE Vorkommen bleibt, die Reihenfolge der uebrigen bleibt, und ein Doppel zaehlt nicht "
        "gegen die Grenze von 10 (die ersten 10 VERSCHIEDENEN gueltigen Namen). Verglichen wird NACH der Behandlung "
        "(zwei Namen, die erst nach Leerraum-Lauf, NFKC, Soft Hyphen usw. gleich sind, sind ein Doppel); der Vergleich "
        "unterscheidet Gross/Klein (der Ausgang loest einen Namen vorher gegen die Identitaeten des Boards auf, das "
        "ist nicht Teil dieser Vektoren). Der Eingang prueft Doppel NICHT und nimmt eine Liste mit exaktem Doppel "
        "an (gemessen); deshalb gibt es dafuer keinen Ablehnungs-Vektor, und die Gegenrichtung 'rohe Liste ungleich "
        "erwartet muss abgelehnt werden' gilt fuer Vektoren, deren Liste sich nur durch exakte Doppel von 'erwartet' "
        "unterscheidet, NICHT (der Eingang darf sie annehmen; das Push-Skript sendet nie Doppel) - aber nur bis zu "
        "10 Eintraegen: eine Liste mit mehr als 10 Eintraegen lehnt der Eingang weiter ab, auch wenn sie nur durch "
        "exakte Doppel von 'erwartet' abweicht. "
        "Am Eingang gilt je Name: Fixpunkt der Behandlung (hoechstens 60), kein '@', kein 'external:'; ein "
        "einziger verletzter Name, mehr als 10 Namen, ein leerer Name oder ein Eintrag, der kein Text ist, lehnt "
        "die ganze Liste ab ('ablehnung' ist 'bearbeiter'; die Meldung beginnt mit 'bearbeiter:' oder "
        "'bearbeiter[<index>]:'). Ist die rohe Liste einer 'erwartet'-Liste nicht gleich, muss der Eingang sie "
        "ablehnen (Gegenrichtung)."
    ),
    "quellen": ["normal", "trim", "leer", "NFKC", "unsichtbar", "zeichenvorrat", "3a-Satzzeichen",
                "verpackt", "max80", "leerraum", "vertraulich", "spalte", "max40", "bearbeiter", "max60", "eingang"],
    "ersatztext": ER,
    "vektoren": V,
}

ziel = sys.argv[1]
with open(ziel, "w", encoding="ascii") as f:
    json.dump(kopf, f, ensure_ascii=True, indent=1)
    f.write("\n")
print(len(V), "Vektoren")
