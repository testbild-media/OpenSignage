# OpenSignage translation glossary and style guide

Binding terminology for the UI in the 24 official EU languages. English (`locales/en`) is the source; `common.json` and `player.json` of every language are the style reference. Use the terms of this table consistently in **all** namespaces (`admin`, `errors`, `fields`, `editor`, `pres`, `devices`, `playlists`, `control`, `client`, …). Extend the table when a new recurring term appears; never use a second word for an existing term.

## General rules

- Placeholders such as `{count}`, `{name}`, `{min}`, `{pct}`, `{fingerprint}` stay exactly as written (the Go test compares the set of placeholders). Do not translate or reorder their names; the words around them may be re-ordered and inflected.
- Short, natural UI language as used in professional software of that language (Microsoft/LibreOffice/Google conventions). No literal word-for-word translation. Buttons and labels are short; hints are complete sentences with a final full stop where English has one.
- Keep untranslated: OpenSignage, PIN, IP, HTTPS, CA, PEM, UDP, URL, Raspberry Pi, Android, iPad/iPhone, Safari, Windows/macOS/Linux, Tailscale, and technical units (`%`, `s`, `ms`). The default password `opensignage` and the state name `CLOSED` stay literal.
- Typography: use the language's normal quotation marks (table below) wherever English has “…”; typographic apostrophe `’` (U+2019), never `'`; ellipsis character `…` where English uses it; keep the arrow `→` and the spaced en dash ` – ` of the source. The `{pct} %` form (space before %) is kept.
- Counts next to a noun: avoid forms that depend on the number (plural cases of Slavic, Baltic, Celtic and Maltese). Prefer colon style (`Slides: {count}` → `Slajdy: {count}`) as in the English source. For `after {days} days` use the plural form that is right for the usual values (7, 14, 30, 90).
- Inflect terms correctly in the sentence (nominative in menus, other cases in sentences). Glossary entries below are dictionary forms; verbs are given in the button form.
- OS menu paths (Android / iOS / Windows settings) use the official localized names of that platform where known; keep the English name otherwise. Do not invent menu names.
- Editor vocabulary (bold, italic, underline, copy, cut, paste, undo, redo, group, align, distribute, rulers, grid, snap, opacity, shadow, outline, layers, …) follows the terms of Microsoft Office / LibreOffice Impress in that language.
- Reuse the wording of `common.json` / `player.json` for generic actions (Delete, Edit, Save, Cancel, Close, Try again, Loading) and for sentences that already exist there.
- Roles: Admin = administrator, Operator = operator, User = user. “Operator view” (`admin.nav.control`) is the live control page; “administration” is the admin area of the web interface. “Take over” is the action that claims a new device by its PIN; “pair” is the resulting connection with the server (“Pair again” resets it).
- Zone state names `OPEN` and `CLOSED` stay literal in all languages (like `CLOSED` above).
- The colon style for counts also applies inside sentences, e.g. `Uses of “{name}”: {count}` instead of `used in {count} places`. Where a sentence needs a number of days or minutes: lt and lv use the abbreviations `d.` / `min` (`prieš {days} d.`, `pirms {days} d.`); hr uses genitive constructions that fit every number (`prije {days} dana`, `nakon {days} dana`); it, mt, pl, ro, sk and sl use the colon style where the value can be 1 (`giorni nel cestino: {days}`, `dní do trvalého odstránenia: {days}`). Irish `lá` and Hungarian `nap` need no plural form.
- Font preview samples (`fonts.sample`) use a native pangram or test phrase of the language plus `0123456789 €`, never the English sentence.
- Decimal examples follow the locale: `4.50` in en, ga and mt, `4,50` in all other languages; the price and quantity inputs accept both separators.
- Hungarian avoids articles and case suffixes directly on placeholders (`… ebből a verzióból: {version}`, `{start} órakor`).
- German: informal `du` only where unavoidable, otherwise infinitive instructions. Mandatory terms: Präsentation, Folie, Medien, Standardwerte, Zonen, Geräte, Playlist, Time Schedule, ausverkauft, nicht verfügbar, verfügbar, veröffentlichen, verwerfen, übernehmen (Gerät), koppeln.

## Typography and address form per language

| Code | Language | Quotation marks | Address form / buttons | Notes |
| --- | --- | --- | --- | --- |
| `bg` | Български | „…“ | Hints and sentences: formal plural imperative (отворете, задайте). Buttons/labels: noun or short imperative as in common.json (Опитай отново). | Cyrillic only, no Latin words except OpenSignage/PIN/IP/HTTPS/CA/PEM. “PIN” stays PIN (ПИН in running text as in player.json). |
| `cs` | Čeština | „…“ | Formal (vykání): aktualizujte, otevřete. Buttons/labels: infinitive (Zrušit). | Slide = snímek. |
| `da` | Dansk | »…« | Informal du in sentences (skal du angive); buttons/labels imperative (Annuller, Prøv igen). | Sentence case; compounds written together (webgrænseflade). |
| `de` | Deutsch | „…“ | Neutral infinitive for instructions (Die PIN eingeben), no Sie; informal du only where unavoidable (as in the existing de editor/pres files). Buttons/labels infinitive (Speichern). | Ellipsis with a space before it (Wird geladen …); ß, not ss; German terms mandatory: see table (Time Schedule stays English). |
| `el` | Ελληνικά | «…» | Formal plural/polite (ενημερώστε, ανοίξτε). Buttons/labels: noun or imperative as in common.json. | Use the Greek question mark (;) for questions. Modern monotonic orthography. |
| `en` | English | “…” | Source language. | Typographic apostrophe ’, arrow →, en dash –. |
| `es` | Español | «…» | Formal usted (actualice, abra). Buttons/labels: infinitive (Reintentar, Cancelar). | Questions and exclamations need ¿…? and ¡…!, e.g. ¿Eliminar «{name}»?. Spain Spanish (ajustes, ordenador). |
| `et` | Eesti | „…“ | Informal singular imperative (uuenda, ava). Buttons/labels: imperative (Tühista). | Compounds written together. |
| `fi` | Suomi | ”…” | Informal singular imperative (päivitä, avaa). Buttons/labels: imperative (Peruuta). | Quotation marks are ”…” (same closing mark both sides). Compounds written together. |
| `fr` | Français | « … » (guillemets with NO-BREAK SPACE U+00A0 inside) | Formal vous (ouvrez, veuillez). Buttons/labels: infinitive (Réessayer, Annuler). | NO-BREAK SPACE (U+00A0) before : ; ! ? and before % in “{pct} %”. Typographic apostrophe ’ (l’appareil). Écran, appareil, liste de lecture. |
| `ga` | Gaeilge | “…” | Informal singular imperative (oscail, socraigh). Buttons/labels as in common.json. | Mind initial mutations (séimhiú/urú) and fadas; typographic apostrophe ’ (B’fhéidir). PIN is written UAP in running text, as in the existing player.json. |
| `hr` | Hrvatski | „…” | Sentences: formal plural (ažurirajte, otvorite); buttons/labels: imperative singular as in common.json (Odustani, Pokušaj ponovno). | web-sučelje, zaslon (screen), uređaj (device). |
| `hu` | Magyar | „…” | Formal (Ön), 3rd person imperative: adja meg, nyissa meg. Buttons/labels as in common.json (Mégse). | Mind the definite/indefinite object and vowel harmony of suffixes after placeholders (use a hyphen-free neutral wording when unsure). |
| `it` | Italiano | «…» | Informal tu imperative (aggiorna, apri). Buttons/labels: imperative/infinitive as in common.json (Riprova, Annulla). | Elision with typographic apostrophe ’ (nell’amministrazione, dell’). |
| `lt` | Lietuvių | „…“ | Formal plural (atnaujinkite, atidarykite). Buttons/labels: infinitive (Atšaukti). | Avoid number-dependent noun inflection next to placeholders (use “Elementai: {count}” style). |
| `lv` | Latviešu | „…“ | Formal plural (atjauniniet, atveriet). Buttons/labels: infinitive (Atcelt). | Avoid number-dependent noun inflection next to placeholders (use colon style). |
| `mt` | Malti | “…” | Informal singular imperative (aġġorna, iftaħ). Buttons/labels as in common.json. | Typographic apostrophe ’ (ta’, m’hemmx, Erġa’). Use ċ ġ ħ ż and għ correctly. |
| `nl` | Nederlands | “…” | Informal je or infinitive instructions (stel in, open … en neem over). Buttons/labels: infinitive (Annuleren). | Compounds written together; pincode may be PIN (as in player.json: “pincode”). |
| `pl` | Polski | „…” | Informal singular imperative (zaktualizuj, otwórz). Buttons/labels: imperative (Anuluj). | Avoid number-dependent noun inflection next to placeholders (use “Slajdy: {count}” style); PIN declines (tego PIN-u). |
| `pt` | Português (PT) | «…» | European Portuguese (PT-PT, AO90). 3rd person imperative without “você” (abra, defina). Buttons/labels: infinitive (Cancelar). | ecrã, utilizador, ficheiro, definições, ligação, “a carregar” (gerund style). Never Brazilian forms (tela, usuário, arquivo). |
| `ro` | Română | „…” | Sentences: formal plural (actualizați, deschideți); buttons/labels: imperative singular as in common.json (Anulează, Încearcă din nou). | Comma-below diacritics ș ț (not cedilla ş ţ). |
| `sk` | Slovenčina | „…“ | Formal (vykanie): aktualizujte, otvorte. Buttons/labels: infinitive (Zrušiť). | Slide = snímka (not snímek, that is Czech). |
| `sl` | Slovenščina | „…“ | Sentences: formal plural (posodobite, odprite); buttons/labels: imperative singular as in common.json (Prekliči, Poskusi znova). | Ellipsis with a space before it (Nalaganje …), as in common.json. |
| `sv` | Svenska | ”…” | Informal du (anger du), imperative for buttons/labels (Avbryt, Försök igen). | Quotation marks are ”…” (same closing mark both sides). Compounds written together. |

## Terminology per language

Column “EN” is the English source term. Verbs are given in the form used for buttons and labels in `common.json` of that language.

### bg – Български

| EN | bg |
| --- | --- |
| presentation | презентация |
| slide | слайд |
| playlist | плейлист |
| media | мултимедия |
| zone | зона |
| device | устройство |
| screen | екран |
| time schedule | времеви график |
| product | продукт |
| category | категория |
| subcategory | подкатегория |
| sold out | изчерпано |
| unavailable | недостъпно |
| available | налично |
| ticker | бягащ текст |
| operator view | изглед за оператор |
| publish | публикуване |
| published | публикувано |
| discard | отхвърляне |
| draft | чернова |
| template | шаблон |
| settings | настройки |
| defaults | стандартни стойности |
| trash | кошче |
| user | потребител |
| administrator | администратор |
| operator | оператор |
| blackout | затъмняване |
| pair | сдвояване |
| take over | поемане |
| certificate | сертификат |
| certificate authority (CA) | удостоверяващ орган (CA) |
| update | актуализация |
| player | плейър |
| dashboard | табло |
| audit log | журнал за одит |
| role | роля |
| administration | администрация |
| web interface | уеб интерфейс |
| remote actions | отдалечени действия |
| entry | запис |
| fit (contain) | побиране |
| fill (cover) | запълване |
| stretch | разтягане |
| rule (time schedule entry) | правило |
| default rule | правило по подразбиране |
| time window | времеви прозорец |
| price override | специална цена |
| base price | основна цена |
| zone overrides (product status) | изключения по зони |
| ticker template | шаблон за бягащ текст |
| archive (verb / noun) | архивирай / архив |
| restore | възстанови |
| delete permanently | изтрий окончателно |
| folder | папка |
| media library | мултимедийна библиотека |
| upload | качване (button: Качи) |
| revision (of a file) | ревизия |
| variant (converted file) | вариант |
| thumbnail | миниатюра |
| poster frame (video still) | заглавен кадър |
| reprocess | обработи отново |
| font | шрифт |
| font family | семейство шрифтове |
| font style | стил |
| font weight | плътност |
| release (software) | версия |
| package | пакет |
| signing key (release key) | ключ за подписване |
| checksum | контролна сума |
| self-test | самопроверка |
| installer | инсталатор |
| repository (GitHub) | хранилище |
| rolled back | върнато (към предишната версия) |

### cs – Čeština

| EN | cs |
| --- | --- |
| presentation | prezentace |
| slide | snímek |
| playlist | playlist |
| media | média |
| zone | zóna |
| device | zařízení |
| screen | obrazovka |
| time schedule | časový plán |
| product | produkt |
| category | kategorie |
| subcategory | podkategorie |
| sold out | vyprodáno |
| unavailable | nedostupné |
| available | dostupné |
| ticker | běžící text |
| operator view | zobrazení operátora |
| publish | publikovat |
| published | publikováno |
| discard | zahodit |
| draft | koncept |
| template | šablona |
| settings | nastavení |
| defaults | výchozí hodnoty |
| trash | koš |
| user | uživatel |
| administrator | správce |
| operator | operátor |
| blackout | zatemnění |
| pair | spárovat |
| take over | převzít |
| certificate | certifikát |
| certificate authority (CA) | certifikační autorita (CA) |
| update | aktualizace |
| player | přehrávač |
| dashboard | přehled |
| audit log | auditní protokol |
| role | role |
| administration | administrace |
| web interface | webové rozhraní |
| remote actions | vzdálené akce |
| entry | položka |
| fit (contain) | přizpůsobit |
| fill (cover) | vyplnit |
| stretch | roztáhnout |
| rule (time schedule entry) | pravidlo |
| default rule | výchozí pravidlo |
| time window | časové okno |
| price override | speciální cena |
| base price | základní cena |
| zone overrides (product status) | odchylky podle zón |
| ticker template | šablona běžícího textu |
| archive (verb / noun) | archivovat / archiv |
| restore | obnovit |
| delete permanently | trvale smazat |
| folder | složka |
| media library | knihovna médií |
| upload | nahrát / nahrávání |
| revision (of a file) | revize |
| variant (converted file) | varianta |
| thumbnail | miniatura |
| poster frame (video still) | titulní obrázek |
| reprocess | znovu zpracovat |
| font | písmo |
| font family | rodina písma |
| font style | řez |
| font weight | tloušťka |
| release (software) | verze |
| package | balíček |
| signing key (release key) | podpisový klíč |
| checksum | kontrolní součet |
| self-test | autotest |
| installer | instalátor |
| repository (GitHub) | repozitář |
| rolled back | vráceno zpět |

### da – Dansk

| EN | da |
| --- | --- |
| presentation | præsentation |
| slide | slide |
| playlist | afspilningsliste |
| media | medier |
| zone | zone |
| device | enhed |
| screen | skærm |
| time schedule | tidsplan |
| product | produkt |
| category | kategori |
| subcategory | underkategori |
| sold out | udsolgt |
| unavailable | ikke tilgængelig |
| available | tilgængelig |
| ticker | ticker |
| operator view | operatørvisning |
| publish | udgiv |
| published | udgivet |
| discard | kassér |
| draft | kladde |
| template | skabelon |
| settings | indstillinger |
| defaults | standardværdier |
| trash | papirkurv |
| user | bruger |
| administrator | administrator |
| operator | operatør |
| blackout | blackout |
| pair | parre |
| take over | overtage |
| certificate | certifikat |
| certificate authority (CA) | certifikatudsteder (CA) |
| update | opdatering |
| player | afspiller |
| dashboard | overblik |
| audit log | revisionslog |
| role | rolle |
| administration | administration |
| web interface | webgrænseflade |
| remote actions | fjernhandlinger |
| entry | post |
| fit (contain) | tilpas |
| fill (cover) | udfyld |
| stretch | stræk |
| rule (time schedule entry) | regel |
| default rule | standardregel |
| time window | tidsvindue |
| price override | særpris |
| base price | grundpris |
| zone overrides (product status) | afvigelser pr. zone |
| ticker template | tickerskabelon |
| archive (verb / noun) | arkivér / arkiv |
| restore | gendan |
| delete permanently | slet permanent |
| folder | mappe |
| media library | mediebibliotek |
| upload | upload |
| revision (of a file) | revision |
| variant (converted file) | variant |
| thumbnail | miniature |
| poster frame (video still) | forsidebillede |
| reprocess | behandl igen |
| font | skrifttype |
| font family | skrifttypefamilie |
| font style | stil |
| font weight | vægt |
| release (software) | version |
| package | pakke |
| signing key (release key) | signeringsnøgle |
| checksum | kontrolsum |
| self-test | selvtest |
| installer | installationsprogram |
| repository (GitHub) | repository |
| rolled back | rullet tilbage |

### de – Deutsch

| EN | de |
| --- | --- |
| presentation | Präsentation |
| slide | Folie |
| playlist | Playlist |
| media | Medien |
| zone | Zone |
| device | Gerät |
| screen | Bildschirm |
| time schedule | Time Schedule |
| product | Produkt |
| category | Kategorie |
| subcategory | Unterkategorie |
| sold out | ausverkauft |
| unavailable | nicht verfügbar |
| available | verfügbar |
| ticker | Ticker |
| operator view | Bedienansicht |
| publish | veröffentlichen |
| published | veröffentlicht |
| discard | verwerfen |
| draft | Entwurf |
| template | Vorlage |
| settings | Einstellungen |
| defaults | Standardwerte |
| trash | Papierkorb |
| user | Benutzer |
| administrator | Administrator |
| operator | Operator |
| blackout | Blackout |
| pair | koppeln |
| take over | übernehmen |
| certificate | Zertifikat |
| certificate authority (CA) | Zertifizierungsstelle (CA) |
| update | Update |
| player | Player |
| dashboard | Dashboard |
| audit log | Audit-Protokoll |
| role | Rolle |
| administration | Verwaltung |
| web interface | Weboberfläche |
| remote actions | Fernaktionen |
| entry | Eintrag |
| fit (contain) | Einpassen |
| fill (cover) | Füllen |
| stretch | Strecken |

### el – Ελληνικά

| EN | el |
| --- | --- |
| presentation | παρουσίαση |
| slide | διαφάνεια |
| playlist | λίστα αναπαραγωγής |
| media | πολυμέσα |
| zone | ζώνη |
| device | συσκευή |
| screen | οθόνη |
| time schedule | χρονοπρόγραμμα |
| product | προϊόν |
| category | κατηγορία |
| subcategory | υποκατηγορία |
| sold out | εξαντλήθηκε |
| unavailable | μη διαθέσιμο |
| available | διαθέσιμο |
| ticker | κυλιόμενο κείμενο |
| operator view | προβολή χειριστή |
| publish | δημοσίευση |
| published | δημοσιευμένο |
| discard | απόρριψη |
| draft | πρόχειρο |
| template | πρότυπο |
| settings | ρυθμίσεις |
| defaults | προεπιλογές |
| trash | κάδος ανακύκλωσης |
| user | χρήστης |
| administrator | διαχειριστής |
| operator | χειριστής |
| blackout | συσκότιση |
| pair | σύζευξη |
| take over | ανάληψη |
| certificate | πιστοποιητικό |
| certificate authority (CA) | αρχή πιστοποίησης (CA) |
| update | ενημέρωση |
| player | πρόγραμμα αναπαραγωγής |
| dashboard | πίνακας ελέγχου |
| audit log | αρχείο καταγραφής ελέγχου |
| role | ρόλος |
| administration | διαχείριση |
| web interface | διεπαφή ιστού |
| remote actions | απομακρυσμένες ενέργειες |
| entry | στοιχείο |
| fit (contain) | προσαρμογή |
| fill (cover) | γέμισμα |
| stretch | τέντωμα |
| rule (time schedule entry) | κανόνας |
| default rule | προεπιλεγμένος κανόνας |
| time window | χρονικό παράθυρο |
| price override | ειδική τιμή |
| base price | βασική τιμή |
| zone overrides (product status) | εξαιρέσεις ανά ζώνη |
| ticker template | πρότυπο κυλιόμενου κειμένου |
| archive (verb / noun) | αρχειοθέτηση / αρχειοθετημένα (not “αρχείο”, which means file) |
| restore | επαναφορά |
| delete permanently | οριστική διαγραφή |
| folder | φάκελος |
| media library | βιβλιοθήκη πολυμέσων |
| upload | μεταφόρτωση |
| revision (of a file) | αναθεώρηση |
| variant (converted file) | παραλλαγή |
| thumbnail | μικρογραφία |
| poster frame (video still) | εικόνα εξωφύλλου |
| reprocess | εκ νέου επεξεργασία |
| font | γραμματοσειρά |
| font family | οικογένεια γραμματοσειρών |
| font style | στυλ |
| font weight | πάχος |
| release (software) | έκδοση |
| package | πακέτο |
| signing key (release key) | κλειδί υπογραφής |
| checksum | άθροισμα ελέγχου |
| self-test | αυτοέλεγχος |
| installer | πρόγραμμα εγκατάστασης |
| repository (GitHub) | αποθετήριο |
| rolled back | επαναφορά (της προηγούμενης έκδοσης) |

### es – Español

| EN | es |
| --- | --- |
| presentation | presentación |
| slide | diapositiva |
| playlist | lista de reproducción |
| media | medios |
| zone | zona |
| device | dispositivo |
| screen | pantalla |
| time schedule | programación horaria |
| product | producto |
| category | categoría |
| subcategory | subcategoría |
| sold out | agotado |
| unavailable | no disponible |
| available | disponible |
| ticker | texto desplazante |
| operator view | vista de operador |
| publish | publicar |
| published | publicado |
| discard | descartar |
| draft | borrador |
| template | plantilla |
| settings | ajustes |
| defaults | valores predeterminados |
| trash | papelera |
| user | usuario |
| administrator | administrador |
| operator | operador |
| blackout | pantalla en negro |
| pair | vincular |
| take over | adoptar |
| certificate | certificado |
| certificate authority (CA) | autoridad de certificación (CA) |
| update | actualización |
| player | reproductor |
| dashboard | panel |
| audit log | registro de auditoría |
| role | rol |
| administration | administración |
| web interface | interfaz web |
| remote actions | acciones remotas |
| entry | elemento |
| fit (contain) | ajustar |
| fill (cover) | rellenar |
| stretch | estirar |
| rule (time schedule entry) | regla |
| default rule | regla predeterminada |
| time window | franja horaria |
| price override | precio especial |
| base price | precio base |
| zone overrides (product status) | excepciones por zona |
| ticker template | plantilla de texto desplazante |
| archive (verb / noun) | archivar / archivo |
| restore | restaurar |
| delete permanently | eliminar definitivamente |
| folder | carpeta |
| media library | biblioteca de medios |
| upload | subir / subida |
| revision (of a file) | revisión |
| variant (converted file) | variante |
| thumbnail | miniatura |
| poster frame (video still) | imagen de portada |
| reprocess | volver a procesar |
| font | fuente |
| font family | familia de fuentes |
| font style | estilo |
| font weight | grosor |
| release (software) | versión |
| package | paquete |
| signing key (release key) | clave de firma |
| checksum | suma de comprobación |
| self-test | autocomprobación |
| installer | instalador |
| repository (GitHub) | repositorio |
| rolled back | revertido |

### et – Eesti

| EN | et |
| --- | --- |
| presentation | esitlus |
| slide | slaid |
| playlist | esitusloend |
| media | meedia |
| zone | tsoon |
| device | seade |
| screen | ekraan |
| time schedule | ajakava |
| product | toode |
| category | kategooria |
| subcategory | alamkategooria |
| sold out | välja müüdud |
| unavailable | pole saadaval |
| available | saadaval |
| ticker | jooksev tekst |
| operator view | operaatori vaade |
| publish | avalda |
| published | avaldatud |
| discard | loobu |
| draft | mustand |
| template | mall |
| settings | seaded |
| defaults | vaikeväärtused |
| trash | prügikast |
| user | kasutaja |
| administrator | administraator |
| operator | operaator |
| blackout | must ekraan |
| pair | siduma |
| take over | võta üle |
| certificate | sertifikaat |
| certificate authority (CA) | sertifitseerimisasutus (CA) |
| update | uuendus |
| player | esitaja |
| dashboard | ülevaade |
| audit log | auditilogi |
| role | roll |
| administration | haldus |
| web interface | veebiliides |
| remote actions | kaugtoimingud |
| entry | kirje |
| fit (contain) | sobita |
| fill (cover) | täida |
| stretch | venita |
| rule (time schedule entry) | reegel |
| default rule | vaikereegel |
| time window | ajaaken |
| price override | erihind |
| base price | põhihind |
| zone overrides (product status) | tsoonipõhised erandid |
| ticker template | jooksva teksti mall |
| archive (verb / noun) | arhiveeri / arhiiv |
| restore | taasta |
| delete permanently | kustuta jäädavalt |
| folder | kaust |
| media library | meediakogu |
| upload | laadi üles / üleslaadimine |
| revision (of a file) | redaktsioon |
| variant (converted file) | variant |
| thumbnail | pisipilt |
| poster frame (video still) | kaanepilt |
| reprocess | töötle uuesti |
| font | font |
| font family | fondiperekond |
| font style | stiil |
| font weight | paksus |
| release (software) | väljalase |
| package | pakett |
| signing key (release key) | allkirjavõti |
| checksum | kontrollsumma |
| self-test | enesetest |
| installer | paigaldaja |
| repository (GitHub) | hoidla |
| rolled back | tagasi pööratud |

### fi – Suomi

| EN | fi |
| --- | --- |
| presentation | esitys |
| slide | dia |
| playlist | soittolista |
| media | media |
| zone | vyöhyke |
| device | laite |
| screen | näyttö |
| time schedule | aikataulu |
| product | tuote |
| category | kategoria |
| subcategory | alakategoria |
| sold out | loppuunmyyty |
| unavailable | ei saatavilla |
| available | saatavilla |
| ticker | vierivä teksti |
| operator view | operaattorinäkymä |
| publish | julkaise |
| published | julkaistu |
| discard | hylkää |
| draft | luonnos |
| template | malli |
| settings | asetukset |
| defaults | oletusarvot |
| trash | roskakori |
| user | käyttäjä |
| administrator | ylläpitäjä |
| operator | operaattori |
| blackout | pimennys |
| pair | muodostaa pari |
| take over | ota käyttöön |
| certificate | varmenne |
| certificate authority (CA) | varmennepalvelu (CA) |
| update | päivitys |
| player | soitin |
| dashboard | kojelauta |
| audit log | tarkastusloki |
| role | rooli |
| administration | hallinta |
| web interface | verkkoliittymä |
| remote actions | etätoiminnot |
| entry | kohde |
| fit (contain) | sovita |
| fill (cover) | täytä |
| stretch | venytä |
| rule (time schedule entry) | sääntö |
| default rule | oletussääntö |
| time window | aikaikkuna |
| price override | erikoishinta |
| base price | perushinta |
| zone overrides (product status) | vyöhykekohtaiset poikkeukset |
| ticker template | vierivän tekstin malli |
| archive (verb / noun) | arkistoi / arkisto |
| restore | palauta |
| delete permanently | poista pysyvästi |
| folder | kansio |
| media library | mediakirjasto |
| upload | lähetä / lähetys (as in admin.json) |
| revision (of a file) | versio |
| variant (converted file) | variantti |
| thumbnail | pikkukuva |
| poster frame (video still) | kansikuva |
| reprocess | käsittele uudelleen |
| font | fontti |
| font family | fonttiperhe |
| font style | tyyli |
| font weight | paino |
| release (software) | versio |
| package | paketti |
| signing key (release key) | allekirjoitusavain |
| checksum | tarkistussumma |
| self-test | itsetesti |
| installer | asennusohjelma |
| repository (GitHub) | repositorio |
| rolled back | kumottu |

### fr – Français

| EN | fr |
| --- | --- |
| presentation | présentation |
| slide | diapositive |
| playlist | liste de lecture |
| media | médias |
| zone | zone |
| device | appareil |
| screen | écran |
| time schedule | planification horaire |
| product | produit |
| category | catégorie |
| subcategory | sous-catégorie |
| sold out | épuisé |
| unavailable | indisponible |
| available | disponible |
| ticker | bandeau défilant |
| operator view | vue opérateur |
| publish | publier |
| published | publié |
| discard | abandonner |
| draft | brouillon |
| template | modèle |
| settings | paramètres |
| defaults | valeurs par défaut |
| trash | corbeille |
| user | utilisateur |
| administrator | administrateur |
| operator | opérateur |
| blackout | écran noir |
| pair | associer |
| take over | adopter |
| certificate | certificat |
| certificate authority (CA) | autorité de certification (CA) |
| update | mise à jour |
| player | lecteur |
| dashboard | tableau de bord |
| audit log | journal d’audit |
| role | rôle |
| administration | administration |
| web interface | interface web |
| remote actions | actions à distance |
| entry | élément |
| fit (contain) | ajuster |
| fill (cover) | remplir |
| stretch | étirer |
| rule (time schedule entry) | règle |
| default rule | règle par défaut |
| time window | plage horaire |
| price override | prix spécial |
| base price | prix de base |
| zone overrides (product status) | exceptions par zone |
| ticker template | modèle de bandeau défilant |
| archive (verb / noun) | archiver / archives |
| restore | restaurer |
| delete permanently | supprimer définitivement |
| folder | dossier |
| media library | médiathèque |
| upload | importer / importation |
| revision (of a file) | révision |
| variant (converted file) | variante |
| thumbnail | miniature |
| poster frame (video still) | image de couverture |
| reprocess | relancer le traitement |
| font | police |
| font family | famille de polices |
| font style | style |
| font weight | graisse |
| release (software) | version |
| package | paquet |
| signing key (release key) | clé de signature |
| checksum | somme de contrôle |
| self-test | autotest |
| installer | programme d’installation |
| repository (GitHub) | dépôt |
| rolled back | version précédente rétablie |

### ga – Gaeilge

| EN | ga |
| --- | --- |
| presentation | cur i láthair |
| slide | sleamhnán |
| playlist | seinmliosta |
| media | meáin |
| zone | crios |
| device | gléas |
| screen | scáileán |
| time schedule | sceideal ama |
| product | táirge |
| category | catagóir |
| subcategory | fochatagóir |
| sold out | díolta amach |
| unavailable | níl ar fáil |
| available | ar fáil |
| ticker | téacs scrollaithe |
| operator view | amharc an oibreora |
| publish | foilsigh |
| published | foilsithe |
| discard | caith amach |
| draft | dréacht |
| template | teimpléad |
| settings | socruithe |
| defaults | réamhshocruithe |
| trash | araid athchúrsála |
| user | úsáideoir |
| administrator | riarthóir |
| operator | oibreoir |
| blackout | dorchú |
| pair | péireáil |
| take over | glac seilbh ar |
| certificate | deimhniú |
| certificate authority (CA) | údarás deimhniúcháin (CA) |
| update | nuashonrú |
| player | seinnteoir |
| dashboard | deais |
| audit log | logchlár iniúchta |
| role | ról |
| administration | riarachán |
| web interface | comhéadan gréasáin |
| remote actions | gníomhartha cianda |
| entry | iontráil |
| fit (contain) | oiriúnaigh |
| fill (cover) | líon |
| stretch | sín |
| rule (time schedule entry) | riail |
| default rule | riail réamhshocraithe |
| time window | fuinneog ama |
| global (scope) | domhanda |
| price override | praghas ionaid |
| base price | bunphraghas |
| ticker template | teimpléad téacs scrollaithe |
| archive (verb / noun) | cartlannaigh / cartlann |
| restore | athchóirigh / athchóiriú |
| delete permanently | scrios go buan |
| folder | fillteán |
| media file | comhad meán |
| upload / download | uaslódáil / íoslódáil |
| replace (file) | athchuir |
| revision (of a file) | athbhreithniú |
| variant (converted file) | malairt |
| thumbnail | mionsamhail |
| poster frame (video still) | fráma clúdaigh |
| process / reprocess | próiseáil / próiseáil arís |
| font | cló |
| font family | clófhoireann |
| font style | stíl |
| font weight | meáchan |
| included (bundled) fonts | clónna ionsuite |
| storage (settings page) | stóras |
| backup | cúltaca |
| release (software) | eisiúint |
| package | pacáiste |
| signing key (release key) | eochair eisiúna |
| signature | síniú |
| checksum | suim sheiceála |
| installer | suiteálaí |
| repository (GitHub) | stór |
| roll back / rolled back | rolladh siar / rollta siar |

### hr – Hrvatski

| EN | hr |
| --- | --- |
| presentation | prezentacija |
| slide | slajd |
| playlist | popis za reprodukciju |
| media | mediji |
| zone | zona |
| device | uređaj |
| screen | zaslon |
| time schedule | vremenski raspored |
| product | proizvod |
| category | kategorija |
| subcategory | potkategorija |
| sold out | rasprodano |
| unavailable | nedostupno |
| available | dostupno |
| ticker | klizni tekst |
| operator view | prikaz za operatera |
| publish | objavi |
| published | objavljeno |
| discard | odbaci |
| draft | skica |
| template | predložak |
| settings | postavke |
| defaults | zadane vrijednosti |
| trash | smeće |
| user | korisnik |
| administrator | administrator |
| operator | operater |
| blackout | zatamnjenje |
| pair | upariti |
| take over | preuzeti |
| certificate | certifikat |
| certificate authority (CA) | certifikacijsko tijelo (CA) |
| update | ažuriranje |
| player | reproduktor |
| dashboard | nadzorna ploča |
| audit log | dnevnik revizije |
| role | uloga |
| administration | administracija |
| web interface | web-sučelje |
| remote actions | udaljene radnje |
| entry | stavka |
| fit (contain) | prilagodi |
| fill (cover) | ispuni |
| stretch | razvuci |
| rule (time schedule entry) | pravilo |
| default rule | zadano pravilo |
| time window | vremenski prozor |
| global (scope) | globalno |
| price override | zamjenska cijena |
| base price | osnovna cijena |
| ticker template | predložak kliznog teksta |
| archive (verb / noun) | arhiviraj / arhiva |
| restore | vrati / vraćanje |
| delete permanently | trajno izbriši |
| folder | mapa |
| media file | medijska datoteka |
| upload / download | prenesi (prijenos) / preuzmi |
| replace (file) | zamijeni |
| revision (of a file) | revizija |
| variant (converted file) | varijanta |
| thumbnail | minijatura |
| poster frame (video still) | naslovna sličica |
| process / reprocess | obrada / ponovno obradi |
| font | font |
| font family | obitelj fontova |
| font style | stil |
| font weight | debljina |
| included (bundled) fonts | ugrađeni fontovi |
| storage (settings page) | pohrana |
| backup | sigurnosna kopija |
| release (software) | izdanje |
| package | paket |
| signing key (release key) | ključ izdanja |
| signature | potpis |
| checksum | kontrolni zbroj |
| installer | instalacijski program |
| repository (GitHub) | repozitorij |
| roll back / rolled back | poništeno |

### hu – Magyar

| EN | hu |
| --- | --- |
| presentation | prezentáció |
| slide | dia |
| playlist | lejátszási lista |
| media | média |
| zone | zóna |
| device | eszköz |
| screen | képernyő |
| time schedule | időbeosztás |
| product | termék |
| category | kategória |
| subcategory | alkategória |
| sold out | elfogyott |
| unavailable | nem elérhető |
| available | elérhető |
| ticker | futószöveg |
| operator view | operátori nézet |
| publish | közzététel |
| published | közzétéve |
| discard | elvetés |
| draft | piszkozat |
| template | sablon |
| settings | beállítások |
| defaults | alapértelmezések |
| trash | kuka |
| user | felhasználó |
| administrator | adminisztrátor |
| operator | operátor |
| blackout | elsötétítés |
| pair | párosítás |
| take over | átvétel |
| certificate | tanúsítvány |
| certificate authority (CA) | hitelesítésszolgáltató (CA) |
| update | frissítés |
| player | lejátszó |
| dashboard | irányítópult |
| audit log | auditnapló |
| role | szerepkör |
| administration | adminisztráció |
| web interface | webes felület |
| remote actions | távoli műveletek |
| entry | elem |
| fit (contain) | illesztés |
| fill (cover) | kitöltés |
| stretch | nyújtás |
| rule (time schedule entry) | szabály |
| default rule | alapértelmezett szabály |
| time window | időablak |
| global (scope) | globális |
| price override | felülírt ár |
| base price | alapár |
| ticker template | futószöveg-sablon |
| archive (verb / noun) | archiválás / archívum |
| restore | visszaállítás |
| delete permanently | végleges törlés |
| folder | mappa |
| media file | médiafájl |
| upload / download | feltöltés / letöltés |
| replace (file) | csere |
| revision (of a file) | revízió |
| variant (converted file) | változat |
| thumbnail | miniatűr |
| poster frame (video still) | állókép |
| process / reprocess | feldolgozás / újrafeldolgozás |
| font | betűtípus |
| font family | betűcsalád |
| font style | stílus |
| font weight | vastagság |
| included (bundled) fonts | mellékelt betűtípusok |
| storage (settings page) | tárhely |
| backup | biztonsági mentés |
| release (software) | kiadás |
| package | csomag |
| signing key (release key) | kiadási kulcs |
| signature | aláírás |
| checksum | ellenőrzőösszeg |
| installer | telepítő |
| repository (GitHub) | tároló |
| roll back / rolled back | visszavonva |

### it – Italiano

| EN | it |
| --- | --- |
| presentation | presentazione |
| slide | diapositiva |
| playlist | playlist |
| media | media |
| zone | zona |
| device | dispositivo |
| screen | schermo |
| time schedule | pianificazione oraria |
| product | prodotto |
| category | categoria |
| subcategory | sottocategoria |
| sold out | esaurito |
| unavailable | non disponibile |
| available | disponibile |
| ticker | testo scorrevole |
| operator view | vista operatore |
| publish | pubblica |
| published | pubblicato |
| discard | scarta |
| draft | bozza |
| template | modello |
| settings | impostazioni |
| defaults | valori predefiniti |
| trash | cestino |
| user | utente |
| administrator | amministratore |
| operator | operatore |
| blackout | oscuramento |
| pair | associare |
| take over | acquisire |
| certificate | certificato |
| certificate authority (CA) | autorità di certificazione (CA) |
| update | aggiornamento |
| player | player |
| dashboard | dashboard |
| audit log | registro di controllo |
| role | ruolo |
| administration | amministrazione |
| web interface | interfaccia web |
| remote actions | azioni remote |
| entry | voce |
| fit (contain) | adatta |
| fill (cover) | riempi |
| stretch | estendi |
| rule (time schedule entry) | regola |
| default rule | regola predefinita |
| time window | fascia oraria |
| global (scope) | globale |
| price override | prezzo sostitutivo |
| base price | prezzo base |
| ticker template | modello del testo scorrevole |
| archive (verb / noun) | archivia / archivio |
| restore | ripristina / ripristino |
| delete permanently | elimina definitivamente |
| folder | cartella |
| media file | file multimediale |
| upload / download | carica / scarica |
| replace (file) | sostituisci |
| revision (of a file) | revisione |
| variant (converted file) | variante |
| thumbnail | miniatura |
| poster frame (video still) | immagine di copertina |
| process / reprocess | elaborazione / rielabora |
| font | font |
| font family | famiglia di font |
| font style | stile |
| font weight | peso |
| included (bundled) fonts | font inclusi |
| storage (settings page) | spazio di archiviazione |
| backup | backup |
| release (software) | release (f.) |
| package | pacchetto |
| signing key (release key) | chiave di release |
| signature | firma |
| checksum | checksum |
| installer | programma di installazione |
| repository (GitHub) | repository |
| roll back / rolled back | rollback eseguito |

### lt – Lietuvių

| EN | lt |
| --- | --- |
| presentation | pateiktis |
| slide | skaidrė |
| playlist | grojaraštis |
| media | medija |
| zone | zona |
| device | įrenginys |
| screen | ekranas |
| time schedule | tvarkaraštis |
| product | produktas |
| category | kategorija |
| subcategory | subkategorija |
| sold out | išparduota |
| unavailable | nepasiekiama |
| available | prieinama |
| ticker | bėganti eilutė |
| operator view | operatoriaus rodinys |
| publish | paskelbti |
| published | paskelbta |
| discard | atmesti |
| draft | juodraštis |
| template | šablonas |
| settings | nustatymai |
| defaults | numatytosios reikšmės |
| trash | šiukšlinė |
| user | naudotojas |
| administrator | administratorius |
| operator | operatorius |
| blackout | aptemdymas |
| pair | susieti |
| take over | perimti |
| certificate | sertifikatas |
| certificate authority (CA) | sertifikavimo institucija (CA) |
| update | naujinimas |
| player | grotuvas |
| dashboard | skydelis |
| audit log | audito žurnalas |
| role | vaidmuo |
| administration | administravimas |
| web interface | žiniatinklio sąsaja |
| remote actions | nuotoliniai veiksmai |
| entry | įrašas |
| fit (contain) | talpinti |
| fill (cover) | užpildyti |
| stretch | ištempti |
| rule (time schedule entry) | taisyklė |
| default rule | numatytoji taisyklė |
| time window | laiko intervalas |
| global (scope) | bendrasis |
| price override | pakaitinė kaina |
| base price | bazinė kaina |
| ticker template | bėgančios eilutės šablonas |
| archive (verb / noun) | archyvuoti / archyvas |
| restore | atkurti / atkūrimas |
| delete permanently | ištrinti visam laikui |
| folder | aplankas |
| media file | medijos failas |
| upload / download | įkelti / atsisiųsti |
| replace (file) | pakeisti |
| revision (of a file) | revizija |
| variant (converted file) | variantas |
| thumbnail | miniatiūra |
| poster frame (video still) | stop kadras |
| process / reprocess | apdorojimas / apdoroti iš naujo |
| font | šriftas |
| font family | šriftų šeima |
| font style | stilius |
| font weight | storis |
| included (bundled) fonts | pridėti šriftai |
| storage (settings page) | saugykla |
| backup | atsarginė kopija |
| release (software) | leidimas |
| package | paketas |
| signing key (release key) | leidimo raktas |
| signature | parašas |
| checksum | kontrolinė suma |
| installer | diegimo programa |
| repository (GitHub) | saugykla |
| roll back / rolled back | atšaukta |

### lv – Latviešu

| EN | lv |
| --- | --- |
| presentation | prezentācija |
| slide | slaids |
| playlist | atskaņošanas saraksts |
| media | multivide |
| zone | zona |
| device | ierīce |
| screen | ekrāns |
| time schedule | laika grafiks |
| product | produkts |
| category | kategorija |
| subcategory | apakškategorija |
| sold out | izpārdots |
| unavailable | nav pieejams |
| available | pieejams |
| ticker | skrejošā līnija |
| operator view | operatora skats |
| publish | publicēt |
| published | publicēts |
| discard | atmest |
| draft | melnraksts |
| template | veidne |
| settings | iestatījumi |
| defaults | noklusējuma vērtības |
| trash | atkritne |
| user | lietotājs |
| administrator | administrators |
| operator | operators |
| blackout | aptumšošana |
| pair | savienot pārī |
| take over | pārņemt |
| certificate | sertifikāts |
| certificate authority (CA) | sertifikācijas iestāde (CA) |
| update | atjauninājums |
| player | atskaņotājs |
| dashboard | informācijas panelis |
| audit log | audita žurnāls |
| role | loma |
| administration | administrācija |
| web interface | tīmekļa saskarne |
| remote actions | attālās darbības |
| entry | ieraksts |
| fit (contain) | ietilpināt |
| fill (cover) | aizpildīt |
| stretch | izstiept |
| rule (time schedule entry) | kārtula |
| default rule | noklusējuma kārtula |
| time window | laika logs |
| global (scope) | globāls |
| price override | aizstājošā cena |
| base price | pamatcena |
| ticker template | skrejošās līnijas veidne |
| archive (verb / noun) | arhivēt / arhīvs |
| restore | atjaunot / atjaunošana |
| delete permanently | dzēst neatgriezeniski |
| folder | mape |
| media file | multivides fails |
| upload / download | augšupielādēt / lejupielādēt |
| replace (file) | aizstāt |
| revision (of a file) | redakcija |
| variant (converted file) | variants |
| thumbnail | sīktēls |
| poster frame (video still) | stopkadrs |
| process / reprocess | apstrāde / apstrādāt atkārtoti |
| font | fonts |
| font family | fontu saime |
| font style | stils |
| font weight | treknums |
| included (bundled) fonts | iekļautie fonti |
| storage (settings page) | krātuve |
| backup | dublējums |
| release (software) | laidiens |
| package | pakotne |
| signing key (release key) | laidiena atslēga |
| signature | paraksts |
| checksum | kontrolsumma |
| installer | instalētājs |
| repository (GitHub) | repozitorijs |
| roll back / rolled back | atsaukts |

### mt – Malti

| EN | mt |
| --- | --- |
| presentation | preżentazzjoni |
| slide | slajd |
| playlist | playlist |
| media | midja |
| zone | żona |
| device | apparat |
| screen | skrin |
| time schedule | skeda tal-ħin |
| product | prodott |
| category | kategorija |
| subcategory | sottokategorija |
| sold out | mibjugħ kollu |
| unavailable | mhux disponibbli |
| available | disponibbli |
| ticker | ticker |
| operator view | veduta tal-operatur |
| publish | ippubblika |
| published | ippubblikat |
| discard | armi |
| draft | abbozz |
| template | mudell |
| settings | issettjar |
| defaults | valuri default |
| trash | landa tal-iskart |
| user | utent |
| administrator | amministratur |
| operator | operatur |
| blackout | blackout |
| pair | tgħaqqad |
| take over | ħu f’idejk |
| certificate | ċertifikat |
| certificate authority (CA) | awtorità taċ-ċertifikazzjoni (CA) |
| update | aġġornament |
| player | plejer |
| dashboard | dashboard |
| audit log | reġistru tal-awditjar |
| role | rwol |
| administration | amministrazzjoni |
| web interface | interfaċċja web |
| remote actions | azzjonijiet mill-bogħod |
| entry | daħla |
| fit (contain) | adatta |
| fill (cover) | imla |
| stretch | iġbed |
| rule (time schedule entry) | regola |
| default rule | regola default |
| time window | intervall tal-ħin |
| global (scope) | globali |
| price override | prezz sostitutiv |
| base price | prezz bażi |
| ticker template | mudell tat-ticker |
| archive (verb / noun) | arkivja / arkivju |
| restore | irrestawra / restawr |
| delete permanently | ħassar għal kollox |
| folder | folder |
| media file | fajl tal-midja |
| upload / download | tella’ (upload) / niżżel |
| replace (file) | issostitwixxi |
| revision (of a file) | reviżjoni |
| variant (converted file) | varjant |
| thumbnail | minjatura |
| poster frame (video still) | frejm tal-qoxra |
| process / reprocess | proċessar / erġa’ pproċessa |
| font | font |
| font family | familja tal-font |
| font style | stil |
| font weight | piż |
| included (bundled) fonts | fonts inklużi |
| storage (settings page) | ħażna |
| backup | kopja ta’ sigurtà |
| release (software) | rilaxx |
| package | pakkett |
| signing key (release key) | ċavetta tar-rilaxx |
| signature | firma |
| checksum | checksum |
| installer | installatur |
| repository (GitHub) | repożitorju |
| roll back / rolled back | mreġġa’ lura |

### nl – Nederlands

| EN | nl |
| --- | --- |
| presentation | presentatie |
| slide | dia |
| playlist | afspeellijst |
| media | media |
| zone | zone |
| device | apparaat |
| screen | scherm |
| time schedule | tijdschema |
| product | product |
| category | categorie |
| subcategory | subcategorie |
| sold out | uitverkocht |
| unavailable | niet beschikbaar |
| available | beschikbaar |
| ticker | lopende tekst |
| operator view | operatorweergave |
| publish | publiceren |
| published | gepubliceerd |
| discard | verwerpen |
| draft | concept |
| template | sjabloon |
| settings | instellingen |
| defaults | standaardwaarden |
| trash | prullenbak |
| user | gebruiker |
| administrator | beheerder |
| operator | operator |
| blackout | zwart scherm |
| pair | koppelen |
| take over | overnemen |
| certificate | certificaat |
| certificate authority (CA) | certificeringsinstantie (CA) |
| update | update |
| player | speler |
| dashboard | dashboard |
| audit log | auditlog |
| role | rol |
| administration | beheer |
| web interface | webinterface |
| remote actions | acties op afstand |
| entry | item |
| fit (contain) | passend |
| fill (cover) | vullend |
| stretch | uitrekken |
| rule (time schedule entry) | regel |
| default rule | standaardregel |
| time window | tijdvenster |
| zone window / global window | zonevenster / globaal venster |
| price override | afwijkende prijs |
| base price | basisprijs |
| ticker template | sjabloon voor lopende tekst |
| archive (verb / noun) | archiveren / archief |
| delete permanently | definitief verwijderen |
| folder | map |
| media library | mediabibliotheek, mediabeheer |
| media file | mediabestand |
| upload / download | uploaden / downloaden |
| revision (of a file) | revisie |
| variant (converted file) | variant |
| thumbnail | miniatuur |
| poster frame (video still) | stilstaand beeld |
| process / reprocess | verwerken / opnieuw verwerken |
| font | lettertype |
| font family | lettertypefamilie |
| font style | stijl |
| font weight | dikte |
| storage (settings page) | opslag |
| backup | back-up |
| release (software) | release |
| repository (GitHub) | opslagplaats |
| roll back / rolled back | terugdraaien / teruggedraaid |

### pl – Polski

| EN | pl |
| --- | --- |
| presentation | prezentacja |
| slide | slajd |
| playlist | playlista |
| media | media |
| zone | strefa |
| device | urządzenie |
| screen | ekran |
| time schedule | harmonogram |
| product | produkt |
| category | kategoria |
| subcategory | podkategoria |
| sold out | wyprzedane |
| unavailable | niedostępne |
| available | dostępne |
| ticker | pasek przewijany |
| operator view | widok operatora |
| publish | opublikuj |
| published | opublikowano |
| discard | odrzuć |
| draft | wersja robocza |
| template | szablon |
| settings | ustawienia |
| defaults | wartości domyślne |
| trash | kosz |
| user | użytkownik |
| administrator | administrator |
| operator | operator |
| blackout | wygaszenie |
| pair | sparować |
| take over | przejąć |
| certificate | certyfikat |
| certificate authority (CA) | urząd certyfikacji (CA) |
| update | aktualizacja |
| player | odtwarzacz |
| dashboard | pulpit |
| audit log | dziennik audytu |
| role | rola |
| administration | administracja |
| web interface | interfejs WWW |
| remote actions | akcje zdalne |
| entry | pozycja |
| fit (contain) | dopasuj |
| fill (cover) | wypełnij |
| stretch | rozciągnij |
| rule (time schedule entry) | reguła |
| default rule | reguła domyślna |
| time window | okno czasowe |
| zone window / global window | okno strefy / okno globalne |
| price override | nadpisana cena |
| base price | cena bazowa |
| ticker template | szablon paska przewijanego |
| archive (verb / noun) | archiwizuj / archiwum |
| delete permanently | usuń trwale |
| folder | folder |
| media library | biblioteka mediów |
| media file | plik multimedialny |
| upload / download | prześlij / pobierz |
| revision (of a file) | rewizja |
| variant (converted file) | wariant |
| thumbnail | miniatura |
| poster frame (video still) | kadr podglądu |
| process / reprocess | przetwarzanie / przetwórz ponownie |
| font | czcionka |
| font family | rodzina czcionek |
| font style | styl |
| font weight | grubość |
| storage (settings page) | pamięć masowa |
| backup | kopia zapasowa |
| release (software) | wydanie |
| repository (GitHub) | repozytorium |
| roll back / rolled back | wycofać / wycofano |

### pt – Português (PT)

| EN | pt |
| --- | --- |
| presentation | apresentação |
| slide | diapositivo |
| playlist | lista de reprodução |
| media | multimédia |
| zone | zona |
| device | dispositivo |
| screen | ecrã |
| time schedule | programação horária |
| product | produto |
| category | categoria |
| subcategory | subcategoria |
| sold out | esgotado |
| unavailable | indisponível |
| available | disponível |
| ticker | texto deslizante |
| operator view | vista do operador |
| publish | publicar |
| published | publicado |
| discard | descartar |
| draft | rascunho |
| template | modelo |
| settings | definições |
| defaults | predefinições |
| trash | reciclagem |
| user | utilizador |
| administrator | administrador |
| operator | operador |
| blackout | ecrã preto |
| pair | emparelhar |
| take over | adotar |
| certificate | certificado |
| certificate authority (CA) | autoridade de certificação (CA) |
| update | atualização |
| player | reprodutor |
| dashboard | painel |
| audit log | registo de auditoria |
| role | função |
| administration | administração |
| web interface | interface web |
| remote actions | ações remotas |
| entry | entrada |
| fit (contain) | ajustar |
| fill (cover) | preencher |
| stretch | esticar |
| rule (time schedule entry) | regra |
| default rule | regra predefinida |
| time window | janela horária |
| zone window / global window | janela da zona / janela global |
| price override | substituição de preço |
| base price | preço base |
| ticker template | modelo de texto deslizante |
| archive (verb / noun) | arquivar / arquivo |
| delete permanently | eliminar permanentemente |
| folder | pasta |
| media library | biblioteca multimédia |
| media file | ficheiro multimédia |
| upload / download | carregar / transferir |
| revision (of a file) | revisão |
| variant (converted file) | variante |
| thumbnail | miniatura |
| poster frame (video still) | imagem de capa |
| process / reprocess | processar / processar novamente |
| font | tipo de letra |
| font family | família |
| font style | estilo |
| font weight | peso |
| storage (settings page) | armazenamento |
| backup | cópia de segurança |
| release (software) | versão |
| repository (GitHub) | repositório |
| roll back / rolled back | reverter / revertido |

### ro – Română

| EN | ro |
| --- | --- |
| presentation | prezentare |
| slide | diapozitiv |
| playlist | listă de redare |
| media | media |
| zone | zonă |
| device | dispozitiv |
| screen | ecran |
| time schedule | program orar |
| product | produs |
| category | categorie |
| subcategory | subcategorie |
| sold out | epuizat |
| unavailable | indisponibil |
| available | disponibil |
| ticker | text derulant |
| operator view | vizualizare operator |
| publish | publică |
| published | publicat |
| discard | renunță |
| draft | ciornă |
| template | șablon |
| settings | setări |
| defaults | valori implicite |
| trash | coș de gunoi |
| user | utilizator |
| administrator | administrator |
| operator | operator |
| blackout | ecran negru |
| pair | asociază |
| take over | preia |
| certificate | certificat |
| certificate authority (CA) | autoritate de certificare (CA) |
| update | actualizare |
| player | player |
| dashboard | panou de control |
| audit log | jurnal de audit |
| role | rol |
| administration | administrare |
| web interface | interfață web |
| remote actions | acțiuni la distanță |
| entry | element |
| fit (contain) | potrivire |
| fill (cover) | umplere |
| stretch | întindere |
| rule (time schedule entry) | regulă |
| default rule | regulă implicită |
| time window | interval orar |
| zone window / global window | interval al zonei / interval global |
| price override | preț modificat |
| base price | preț de bază |
| ticker template | șablon de text derulant |
| archive (verb / noun) | arhivează / arhivă |
| delete permanently | șterge definitiv |
| folder | folder |
| media library | biblioteca media |
| media file | fișier media |
| upload / download | încarcă / descarcă |
| revision (of a file) | revizie |
| variant (converted file) | variantă |
| thumbnail | miniatură |
| poster frame (video still) | cadru static |
| process / reprocess | procesare / reprocesează |
| font | font |
| font family | familie de fonturi |
| font style | stil |
| font weight | grosime |
| storage (settings page) | stocare |
| backup | copie de rezervă |
| release (software) | versiune |
| repository (GitHub) | depozit |
| roll back / rolled back | revenire / revenit |

### sk – Slovenčina

| EN | sk |
| --- | --- |
| presentation | prezentácia |
| slide | snímka |
| playlist | playlist |
| media | médiá |
| zone | zóna |
| device | zariadenie |
| screen | obrazovka |
| time schedule | časový plán |
| product | produkt |
| category | kategória |
| subcategory | podkategória |
| sold out | vypredané |
| unavailable | nedostupné |
| available | dostupné |
| ticker | bežiaci text |
| operator view | zobrazenie operátora |
| publish | zverejniť |
| published | zverejnené |
| discard | zahodiť |
| draft | koncept |
| template | šablóna |
| settings | nastavenia |
| defaults | predvolené hodnoty |
| trash | kôš |
| user | používateľ |
| administrator | správca |
| operator | operátor |
| blackout | zatemnenie |
| pair | spárovať |
| take over | prevziať |
| certificate | certifikát |
| certificate authority (CA) | certifikačná autorita (CA) |
| update | aktualizácia |
| player | prehrávač |
| dashboard | prehľad |
| audit log | auditný záznam |
| role | rola |
| administration | administrácia |
| web interface | webové rozhranie |
| remote actions | vzdialené akcie |
| entry | položka |
| fit (contain) | prispôsobiť |
| fill (cover) | vyplniť |
| stretch | roztiahnuť |
| rule (time schedule entry) | pravidlo |
| default rule | predvolené pravidlo |
| time window | časové okno |
| zone window / global window | okno zóny / globálne okno |
| price override | upravená cena |
| base price | základná cena |
| ticker template | šablóna bežiaceho textu |
| archive (verb / noun) | archivovať / archív |
| delete permanently | natrvalo odstrániť |
| folder | priečinok |
| media library | knižnica médií |
| media file | mediálny súbor |
| upload / download | nahrať / stiahnuť |
| revision (of a file) | revízia |
| variant (converted file) | variant |
| thumbnail | miniatúra |
| poster frame (video still) | titulný obrázok |
| process / reprocess | spracovať / znova spracovať |
| font | písmo |
| font family | rodina písma |
| font style | rez |
| font weight | hrúbka |
| storage (settings page) | úložisko |
| backup | záloha |
| release (software) | vydanie |
| repository (GitHub) | repozitár |
| roll back / rolled back | vrátiť späť / vrátené späť |

### sl – Slovenščina

| EN | sl |
| --- | --- |
| presentation | predstavitev |
| slide | diapozitiv |
| playlist | seznam predvajanja |
| media | predstavnost |
| zone | cona |
| device | naprava |
| screen | zaslon |
| time schedule | časovni razpored |
| product | izdelek |
| category | kategorija |
| subcategory | podkategorija |
| sold out | razprodano |
| unavailable | ni na voljo |
| available | na voljo |
| ticker | tekoče besedilo |
| operator view | pogled operaterja |
| publish | objavi |
| published | objavljeno |
| discard | zavrzi |
| draft | osnutek |
| template | predloga |
| settings | nastavitve |
| defaults | privzete vrednosti |
| trash | koš |
| user | uporabnik |
| administrator | skrbnik |
| operator | operater |
| blackout | zatemnitev |
| pair | seznaniti |
| take over | prevzeti |
| certificate | potrdilo |
| certificate authority (CA) | overitelj potrdil (CA) |
| update | posodobitev |
| player | predvajalnik |
| dashboard | nadzorna plošča |
| audit log | revizijska sled |
| role | vloga |
| administration | skrbništvo |
| web interface | spletni vmesnik |
| remote actions | oddaljena dejanja |
| entry | vnos |
| fit (contain) | prilagodi |
| fill (cover) | zapolni |
| stretch | raztegni |
| rule (time schedule entry) | pravilo |
| default rule | privzeto pravilo |
| time window | časovno okno |
| zone window / global window | okno cone / globalno okno |
| price override | spremenjena cena |
| base price | osnovna cena |
| ticker template | predloga tekočega besedila |
| archive (verb / noun) | arhiviraj / arhiv |
| delete permanently | trajno izbriši |
| folder | mapa |
| media library | knjižnica predstavnosti |
| media file | predstavnostna datoteka |
| upload / download | naloži / prenesi |
| revision (of a file) | revizija |
| variant (converted file) | varianta |
| thumbnail | sličica |
| poster frame (video still) | naslovna slika |
| process / reprocess | obdelava / znova obdelaj |
| font | pisava |
| font family | družina pisave |
| font style | slog |
| font weight | debelina |
| storage (settings page) | shramba |
| backup | varnostna kopija |
| release (software) | izdaja |
| repository (GitHub) | skladišče |
| roll back / rolled back | povrniti / povrnjeno |

### sv – Svenska

| EN | sv |
| --- | --- |
| presentation | presentation |
| slide | slide |
| playlist | spellista |
| media | media |
| zone | zon |
| device | enhet |
| screen | skärm |
| time schedule | tidsschema |
| product | produkt |
| category | kategori |
| subcategory | underkategori |
| sold out | slutsåld |
| unavailable | inte tillgänglig |
| available | tillgänglig |
| ticker | löpande text |
| operator view | operatörsvy |
| publish | publicera |
| published | publicerad |
| discard | förkasta |
| draft | utkast |
| template | mall |
| settings | inställningar |
| defaults | standardvärden |
| trash | papperskorg |
| user | användare |
| administrator | administratör |
| operator | operatör |
| blackout | svart skärm |
| pair | para |
| take over | ta över |
| certificate | certifikat |
| certificate authority (CA) | certifikatutfärdare (CA) |
| update | uppdatering |
| player | spelare |
| dashboard | översikt |
| audit log | granskningslogg |
| role | roll |
| administration | administration |
| web interface | webbgränssnitt |
| remote actions | fjärråtgärder |
| entry | post |
| fit (contain) | anpassa |
| fill (cover) | fyll |
| stretch | sträck |
| rule (time schedule entry) | regel |
| default rule | standardregel |
| time window | tidsfönster |
| zone window / global window | zonfönster / globalt fönster |
| price override | avvikande pris |
| base price | baspris |
| ticker template | mall för löpande text |
| archive (verb / noun) | arkivera / arkiv |
| delete permanently | ta bort permanent |
| folder | mapp |
| media library | mediebibliotek |
| media file | mediefil |
| upload / download | ladda upp / ladda ner |
| revision (of a file) | revision |
| variant (converted file) | variant |
| thumbnail | miniatyr |
| poster frame (video still) | stillbild |
| process / reprocess | bearbeta / bearbeta igen |
| font | teckensnitt |
| font family | teckensnittsfamilj |
| font style | stil |
| font weight | vikt |
| storage (settings page) | lagring |
| backup | säkerhetskopia |
| release (software) | utgåva |
| repository (GitHub) | lagringsplats |
| roll back / rolled back | återställa / återställd |

## Term notes

- **slide**: a page of a presentation
- **media**: uploaded files (images, videos, audio); navigation label
- **zone**: a group of screens of one venue area
- **device**: a player hardware (Raspberry Pi, PC) paired with the server
- **screen**: the display that shows the content
- **time schedule**: rules that switch playlists/prices by time; German keeps the English term
- **product**: item of the drink/food menu
- **sold out**: product status
- **unavailable**: product status
- **available**: product status
- **ticker**: scrolling text line on a slide
- **operator view**: live control page for bar staff (open/closed, sold out, ticker, blackout)
- **publish**: button / action; the opposite of keeping a draft
- **published**: state
- **discard**: throw away unpublished changes
- **draft**: unpublished working version
- **template**: saved presentation used as a starting point
- **defaults**: default values
- **trash**: deleted items kept for a while
- **user**: account / role
- **administrator**: role “Admin”; also the person who administers the server
- **operator**: role between admin and user (edits content)
- **blackout**: all screens of a zone go dark on demand
- **pair**: pairing a device with the server
- **take over**: claim a new device on the server with its PIN
- **certificate**: TLS certificate
- **certificate authority (CA)**: always followed/abbreviated as “CA”
- **update**: software update
- **player**: software on the device that plays the content
- **dashboard**: start page of the administration
- **audit log**: log of who changed what
- **role**: admin / operator / user
- **administration**: the admin area of the web interface (“in the administration”)
- **remote actions**: reboot, screenshot, … sent to a device
- **entry**: one item in a playlist
- **fit (contain)**: image/video scaling: whole image visible
- **fill (cover)**: image/video scaling: edges may be cut
- **stretch**: image/video scaling: distorted to the area
- **rule (time schedule entry)**: one entry of a time schedule (a time window with a playlist and/or prices)
- **default rule**: rule that applies when no time window is active
- **time window**: days and times during which a rule applies
- **zone window / global window**: time window for one zone / for all zones; a zone window has priority over a global window
- **global (scope)**: applies to all zones
- **price override**: price of a product that replaces the base price while a time window is active
- **base price**: normal price of a product when no price override is active
- **zone overrides (product status)**: product status set for one zone instead of the global status
- **ticker template**: saved ticker text that can be reused
- **archive (verb / noun)**: archived products are hidden on all screens and can be restored; the archive is the list of them (el: not “αρχείο”, which means file)
- **restore**: bring back an archived or deleted item, an older revision or a backup
- **delete permanently**: delete without the trash / archive; cannot be undone
- **folder**: folder in the media library
- **media library**: the media manager (`/media`)
- **media file**: one uploaded file in the media library
- **upload / download**: sending a file to the server / fetching a file from it; follow the existing `cert.upload` wording of each language
- **replace (file)**: “Replace file” uploads a new version of a media file; the old one is kept as a revision
- **revision (of a file)**: older version of a media file kept after “Replace file”
- **variant (converted file)**: converted derivative of a media file (1080p video, 1920 px image, …)
- **poster frame (video still)**: still image shown for a video
- **process / reprocess**: server-side conversion of an uploaded media file into variants and thumbnails; reprocess runs it again
- **font / font family / font style / font weight**: uploaded or included fonts (`media.fonts.*`); weight is the numeric font weight, style is normal/italic
- **included (bundled) fonts**: fonts shipped with OpenSignage; their family names are reserved
- **storage (settings page)**: disk space of the server or the device
- **backup**: full backup of the server (database, media, fonts, server keys)
- **release (software)**: downloadable OpenSignage version on the updates page (`updates.*`); several languages use their word for “version” (pt: *versão*; rephrase where release and version would collide)
- **package**: installable file of a release for one platform
- **signing key (release key) / signature / checksum**: public key, signature and checksum used to verify a release
- **self-test**: check a new version runs after an update; if it fails, the update is rolled back
- **installer**: OpenSignage installation program of a release
- **repository (GitHub)**: the GitHub repository that releases can be downloaded from
- **roll back / rolled back**: an update that failed and was reverted to the previous version
