## Proje

jiramcp, Go ile yazılmış bir MCP sunucusudur. Jira'da bir ekip projesine, adı verilen kişi adına ticket açar ve yönetir. Ayrıntılar README.md içinde.

- `cmd/server/`: giriş noktası, transport seçimi (stdio/http), yaşam döngüsü.
- `internal/config/`: ortam değişkenleri ve team mapping yükleme/doğrulama.
- `internal/jira/`: Jira REST istemcisi (Cloud v3 + ADF, Server/DC v2). `Client` arayüzünün arkasında; MCP katmanı somut istemciyi bilmez.
- `internal/mcpserver/`: MCP araçları, mapping yetki kontrolü, kimlik çözümleme, auth middleware.
- `internal/health/`: önbellekli liveness/readiness.
- `helm/jiramcp/`: Kubernetes chart.

## Komutlar

- `make all`: tidy, vet, test, build.
- `make test`: `go test ./... -race -count=1`.
- `make helm-lint helm-template`: chart kontrolü.
- Değişiklikten sonra en az `go vet ./...` ve `go test -race ./...` çalıştır.

## Güvenlik kuralları

- Çağıranın proje policy'si (`internal/access`) her issue işleminin yetki sınırıdır. Issue key alan her araç `requireAllowedIssue` ile kontrol edilir ve Jira'nın döndürdüğü güncel key kullanılır. Handler'lar Jira client'ı ve policy'yi `s.principal(ctx)` üzerinden alır, sunucu alanlarından değil. Yeni bir araç bu kuralları atlamamalı.
- Kimlik çözümlemesi kapalı başarısız olur: belirsiz ya da bulunamayan kullanıcı için tahmin yapma, hata dön.
- Kimlik bilgileri yalnızca ortamdan okunur ve asla loglanmaz.
- Stdio modunda stdout MCP trafiğidir; log yalnızca stderr'e gider.

## Kod tasarımı

- **SOLID:** Paket, tip ve fonksiyonların tek bir değişim nedeni olsun (SRP). Gerçek genişleme ihtiyacında davranışı mevcut kodu dağıtmadan genişlet (OCP). Arayüzü uygulayan tipler beklenen sözleşmeyi korusun (LSP). Çağıranları kullanmadıkları geniş arayüzlere bağımlı kılma (ISP). MCP araç mantığını somut HTTP/Jira ayrıntılarına gereksiz yere bağlama (DIP). Bu ilkeler için sırf biçimsel uyum adına yeni katmanlar oluşturma.
- **DRY:** Aynı iş kuralı veya bilgiyi birden çok yerde tutma; ortak davranışı tek kaynağa taşı. Yalnızca görünüşte benzer, farklı nedenlerle değişen kodu zorla birleştirme.
- **KISS:** Doğrudan okunabilen en basit çözümü seç. Açık isimler ve kısa, odaklı fonksiyonlar kullan; gereksiz dolaylılık ve durum yönetiminden kaçın.
- **YAGNI:** Bugünkü gereksinim için gerekmeyen özellik, ayar veya soyutlamayı ekleme. Gereken yeniden düzenlemeyi ve testleri erteleme.

## Karmaşıklık ve yeniden kullanım

- **Tek sorumluluk:** Yapılandırma, Jira iletişimi, MCP araç mantığı ve transport gibi farklı değişim nedenlerini ilgili paket sınırlarında ayır. Bir fonksiyonun ne yaptığını adıyla açıklayabil.
- **Bilişsel karmaşıklık:** İç içe koşul ve döngüler okuyucunun takip yükünü artırır. Erken dönüş, açık ara adımlar ve iyi adlandırılmış yardımcı fonksiyonlarla akışı sadeleştir; karmaşıklığı yalnızca başka fonksiyona taşıma.
- **Döngüsel karmaşıklık:** Bağımsız karar yollarını azalt; anlamlı dalları test et. Metrikleri inceleme sinyali olarak kullan, bağlamdan bağımsız sayısal hedef uğruna okunabilirliği bozma.
- **Yeniden kullanılabilirlik:** Tekrarlanan gerçek davranışı küçük, odaklı fonksiyon ve tiplerle paylaş. Bağımlılıkları açık tut; dışarıdan parametre veya arayüz olarak ver. Henüz tek kullanımlık kod için genel çatı kurma.
- Değişen davranışı ilgili testlerle doğrula: MCP araçları için `fakeClient`, REST istemcisi için `httptest` kullan. `gofmt` ve standart Go adlandırma düzenini izle; yorumlar "ne"yi değil "neden"i anlatsın.
