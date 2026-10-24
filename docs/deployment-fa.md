# نصب و راه‌اندازی TG روی ایران و خارج

**نسخهٔ راهنما: ۲۰۲۶/۱۰/۲۴** · [معرفی مدیریتی و معماری](../README-fa.md) · [آخرین آزمون ظرفیت](one-gig-fa.md) · [راهنمای انگلیسی عملیات](operations.md)

این راهنما برای دو سرور لینوکسی با systemd و ترافیک IPv4 است. سرور **X = ایران / edge** و سرور **Y = خارج / exit**. مسیر پیشنهادی: WireGuard کرنل داخل TG، با دو حامل صریح CONNECT روی TCP و QUIC-stream روی UDP.

**وضعیت پذیرش:** نصب و پنل واقعی کاربر هنوز آزموده نشده‌اند. میانگین ۱٫۰۵ گیگابیت محلی، پذیرش سرعت پیوسته و کیفیت بازی را پاس نکرده است. این دستورها راهنمای استقرار نسخهٔ توسعه‌اند؛ وضعیت کنترل محلی آن‌ها در انتهای سند آمده است.

## فهرست اجرا

۱. نصب باینری روی دو سرور؛ ۲. تولید یک جفت هویت روی دستگاه مورد اعتماد؛ ۳. تنظیم حامل‌ها و توزیع فایل هر نقش؛ ۴. اجرای TG؛ ۵. ساخت WireGuard؛ ۶. NAT خارج؛ ۷. اتصال Xray/X-UI یا مسیریابی زیرشبکهٔ کاربران؛ ۸. سنجش و پایش.

## ۱. پیش‌نیازها و مقادیر نمونه

| مورد | مقدار این راهنما |
|---|---|
| ایران | `IRAN_IP`؛ نشانی واقعی سرور X |
| خارج | `203.0.113.20`؛ نشانی مستندسازی، حتماً با IP واقعی Y عوض شود |
| هویت TLS خارج | `exit.example.net`؛ هنگام تولید با نام انتخابی خود جایگزین شود |
| رابط WireGuard | `wg-tg`؛ باید نامی آزاد باشد |
| IP داخل تانل | ایران `10.77.0.1/30`، خارج `10.77.0.2/30`؛ بدون تداخل با شبکهٔ موجود |
| جدول/اولویت policy routing ایران | جدول `177` و اولویت `17700`؛ باید آزاد باشند |
| حامل عمومی خارج | TCP/8443 و UDP/8444 |

ابزارهای لازم: Git، Go نسخهٔ **۱٫۲۶ یا بالاتر** برای ساخت، Python 3 برای ویرایش نمونه، `wireguard-tools`، `iproute2`، curl و ابزار فایروال متناسب با میزبان. دسترسی sudo برای نصب باینری، سرویس و شبکه لازم است؛ خود daemon با کاربر محدود `tg` اجرا می‌شود. در میزبان کانتینری، پشتیبانی WireGuard کرنل و مجوز مدیریت شبکه باید توسط میزبان فراهم شده باشد.

این دستورات برای راه‌اندازی تازه‌اند. پیش از اجرا، پورت‌ها، رابط، نام کاربر و فایل‌های موجود را بررسی کنید. مسیر مدیریت SSH و آدرس‌های عمومی حامل باید روی اینترنت اصلی باقی بمانند.

## ۲. نصب تک‌خطی TG روی هر دو سرور

پس از نصب پیش‌نیازها، دستور زیر را روی X و Y اجرا کنید. پوشهٔ `tg-src` نباید از قبل موجود باشد:

```sh
git clone --filter=blob:none --no-checkout https://github.com/DashSaman/tg.git tg-src && git -C tg-src checkout --detach 95eb69e561d40a9cc1d7ecf6b223f9fb9fef6baf && (cd tg-src && CGO_ENABLED=0 go build -trimpath -o tg ./cmd/tg) && sudo install -m 0755 tg-src/tg /usr/local/bin/tg && tg version
```

دستور، **نسخهٔ مشخص از سورس** را می‌سازد؛ اسکریپت نصب از راه دور اجرا نمی‌کند. دریافت سورس و وابستگی‌های Go به شبکه نیاز دارد. نصب اولیهٔ Go و ساخت شبکهٔ WireGuard جزو این تک‌خط نیست. خروجی `version` در این مرحله همچنان نسخهٔ توسعه را نشان می‌دهد؛ commit مبنا را نیز ثبت کنید.

کد اجرایی این commit با نسخهٔ آزموده‌شدهٔ `e9feb4f` یکسان است؛ تغییرات مرحلهٔ `95eb69e` ابزار سنجش و مستندات بودند. نتایج ظرفیت را به نسخه یا تنظیم دیگری تعمیم ندهید.

## ۳. تولید هویت‌ها روی دستگاه مورد اعتماد

یک بار روی دستگاه مورد اعتماد دارای TG اجرا کنید؛ کل جفت هویت را جداگانه روی هر سرور نسازید:

```sh
umask 077
tg init --dir tg-private --server-name exit.example.net --redundant
```

پوشهٔ `tg-private` باید جدید باشد. نام سرور را پیش از تولید انتخاب کنید. اگر حامل با IP وصل شود، نام TLS همچنان با گواهی تطبیق داده می‌شود؛ نام هویت الزاماً همان مقدار `address` نیست. اعتبارسنجی TLS را خاموش نکنید.

### انتخاب CONNECT + QUIC و آدرس دو سر

در همان پوشهٔ کاری، IP نمونه را با IPv4 واقعی خارج عوض کنید و سپس اجرا کنید:

```sh
EXIT_HOST=203.0.113.20
python3 - "$EXIT_HOST" <<'PY'
import json
import sys
from pathlib import Path

host = sys.argv[1]
for role in ("edge", "exit"):
    path = Path("tg-private") / f"{role}.json"
    c = json.loads(path.read_text())
    c.update(
        redundant=True,
        stream_packets=True,
        tls_record_batch=True,
        ordered_output=False,
        replica_delay_ms=10,
        retained_packets=8192,
        max_packet_bytes=2048,
    )
    for carrier in c["carriers"]:
        if carrier["name"] == "web":
            carrier["kind"] = "https-connect"
            carrier["cost_ms"] = 0
            port = 8443
        else:
            carrier["kind"] = "quic-stream"
            carrier["cost_ms"] = 20
            port = 8444
        carrier["address"] = (
            f"0.0.0.0:{port}" if role == "exit"
            else f"{host}:{port}"
        )
    path.write_text(json.dumps(c, indent=2) + "\n")
PY

tg check --config tg-private/edge.json
tg check --config tg-private/exit.json
```

این ویرایش برای جفت تازهٔ تولیدشده است؛ تنظیمات نصب قبلی را بی‌بررسی بازنویسی نکنید. مقدار تأخیر پشتیبان ۱۰ میلی‌ثانیه حفظ شده است؛ مقدار صفر در آزمون بلند اخیر تشخیصی بود. در RTT بالاتر از مهلت ACK، هر دو حامل ممکن است نسخهٔ بسته را بفرستند و پهنای باند بیشتری مصرف شود.

`check` اعتبار JSON و گواهی محلی را می‌سنجد، نه دسترسی همتا و سلامت اینترنت خروجی. در این نسخه، بررسی توکن CONNECT در زمان راه‌اندازی حامل انجام می‌شود؛ صرف موفقیت `check`، درستی فایل توکن CONNECT را ثابت نمی‌کند. گواهی، کلید، CA و `web.token` باید همگی برای کاربر سرویس قابل خواندن باشند.

### تفکیک فایل هر نقش

```sh
install -d -m 0700 provision/edge provision/exit
install -m 0600 tg-private/edge.json tg-private/edge.crt tg-private/edge.key tg-private/ca.crt tg-private/web.token provision/edge/
install -m 0600 tg-private/exit.json tg-private/server.crt tg-private/server.key tg-private/ca.crt tg-private/web.token provision/exit/
```

| مقصد | فایل‌های لازم |
|---|---|
| X / ایران | `edge.json`، `edge.crt`، `edge.key`، `ca.crt`، `web.token` |
| Y / خارج | `exit.json`، `server.crt`، `server.key`، `ca.crt`، `web.token` |
| نگه‌داری خارج از سرورها | `ca.key` و نسخهٔ پشتیبان صدور |

فایل‌های هر نقش را با SSH/SFTP امن به همان سرور منتقل کنید. نمونهٔ زیر فرض می‌کند ورود SSH با کاربر `operator` از قبل برقرار است و `tg-incoming` در مقصد وجود ندارد؛ نام کاربر و آدرس‌ها را عوض کنید:

```sh
scp -r provision/edge operator@IRAN_IP:tg-incoming
scp -r provision/exit operator@EXIT_IP:tg-incoming
```

`tg-incoming` در مراحل بعد پوشهٔ دریافت همان نقش داخل home کاربر است. کل پوشهٔ `tg-private` را روی دو میزبان کپی نکنید. کلیدهای خصوصی و توکن را در چت، Git یا تیکت قرار ندهید. گواهی برگ یک‌سال اعتبار دارد و تمدید خودکار ارائه نشده است.

## ۴. نصب سرویس و اجرای TG

روی هر دو سرور از پوشه‌ای که `tg-src` و `tg-incoming` در آن‌اند:

```sh
id -u tg >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin tg
sudo install -d -o root -g tg -m 0750 /etc/tg
sudo install -m 0644 tg-src/deploy/tg@.service /etc/systemd/system/tg@.service
sudo systemctl daemon-reload
```

نام کاربر `tg` باید مختص همین سرویس باشد. اگر از قبل متعلق به سرویس دیگری است، پیش از استفاده نام کاربر و unit را هماهنگ تغییر دهید.

**فقط روی X / ایران:**

```sh
sudo install -o root -g tg -m 0640 tg-incoming/edge.json tg-incoming/edge.crt tg-incoming/edge.key tg-incoming/ca.crt tg-incoming/web.token /etc/tg/
sudo -u tg /usr/local/bin/tg check --config /etc/tg/edge.json
sudo systemctl enable --now tg@edge
```

**فقط روی Y / خارج:**

```sh
sudo install -o root -g tg -m 0640 tg-incoming/exit.json tg-incoming/server.crt tg-incoming/server.key tg-incoming/ca.crt tg-incoming/web.token /etc/tg/
sudo -u tg /usr/local/bin/tg check --config /etc/tg/exit.json
sudo systemctl enable --now tg@exit
```

unit موجود از کاربر محدود، محدودیت دسترسی فایل و `Restart=on-failure` استفاده می‌کند. TG برای ساخت رابط شبکه یا تغییر route دسترسی ندارد. نگه‌داشتن یک حامل سالم مسئول خود TG است؛ restart خودکار systemd جای آن را نمی‌گیرد.

### پورت و فایروال

| محل | پورت | دسترسی مورد نیاز |
|---|---|---|
| Y / خارج | TCP/8443 | ورودی CONNECT از IP خروجی واقعی X |
| Y / خارج | UDP/8444 | ورودی QUIC از IP خروجی واقعی X |
| X / ایران | UDP/31820 | فقط loopback؛ WireGuard به TG |
| Y / خارج | UDP/51820 | WireGuard مقصد؛ دسترسی عمومی لازم ندارد |
| X / ایران | TCP/19090 | پایش فقط loopback |
| Y / خارج | TCP/19091 | پایش فقط loopback |

در فایروال میزبان و دیتاسنتر، حامل‌ها و ترافیک برگشت را مجاز کنید. اگر X پشت NAT است، IP خروجی واقعی آن ملاک است. TG خارج در این نمونه روی `0.0.0.0` گوش می‌دهد؛ محدودسازی مبدأ را فایروال انجام می‌دهد. WireGuard لینوکس روی پورت انتخابی گوش می‌دهد؛ برای ۵۱۸۲۰ در این استقرار، دسترسی خارجی را جداگانه ببندید و loopback را مجاز نگه دارید.

پورت ۸۴۴۳ برای اجرای بدون مجوز پورت‌های ویژه انتخاب شده است؛ تغییر به ۴۴۳ نیازمند طراحی bind/forwarding و بررسی تداخل با وب‌سرور است. CDN یا reverse proxy باید CONNECT دوطرفه و mTLS موردنیاز را واقعاً پشتیبانی کند؛ عبور عادی HTTPS کافی نیست.

## ۵. WireGuard داخل TG

روی هر میزبان، کلید مستقل بسازید. دستور زیر فقط برای نصب تازه است و فایل موجود را بازنویسی نمی‌کند:

```sh
sudo install -d -m 0700 /etc/wireguard
sudo sh -c 'set -eu; umask 077; test ! -e /etc/wireguard/tg.key; test ! -e /etc/wireguard/tg.pub; wg genkey > /etc/wireguard/tg.key; wg pubkey < /etc/wireguard/tg.key > /etc/wireguard/tg.pub'
sudo cat /etc/wireguard/tg.pub
```

فقط کلید عمومی را با همتا مبادله کنید. در فایل‌های زیر، جای‌نگهدارها باید با کلیدهای واقعی همان سرور و همتا جایگزین شوند. کلید خصوصی را فقط در ویرایشگر امن خود سرور وارد کنید.

ابتدا در ایران بررسی کنید جدول و اولویت انتخابی آزاد باشند:

```sh
ip -4 rule show
ip -4 route show table 177
```

اگر جدول یا اولویت استفاده شده‌اند، یک مقدار آزاد انتخاب و همهٔ موارد متناظر زیر را هماهنگ عوض کنید. `wg-tg` و زیرشبکهٔ `10.77.0.0/30` نیز نباید با شبکهٔ موجود تداخل داشته باشند.

### X / ایران: `/etc/wireguard/wg-tg.conf`

```ini
[Interface]
Address = 10.77.0.1/30
PrivateKey = IRAN_PRIVATE_KEY
ListenPort = 51821
MTU = 1280
Table = off
PostUp = ip -4 route add default dev %i table 177
PostUp = ip -4 rule add pref 17700 from 10.77.0.1/32 lookup 177
PreDown = ip -4 rule del pref 17700 from 10.77.0.1/32 lookup 177
PreDown = ip -4 route del default dev %i table 177

[Peer]
PublicKey = EXIT_PUBLIC_KEY
AllowedIPs = 0.0.0.0/0
Endpoint = 127.0.0.1:31820
PersistentKeepalive = 25
```

### Y / خارج: `/etc/wireguard/wg-tg.conf`

```ini
[Interface]
Address = 10.77.0.2/30
PrivateKey = EXIT_PRIVATE_KEY
ListenPort = 51820
MTU = 1280
Table = off

[Peer]
PublicKey = IRAN_PUBLIC_KEY
AllowedIPs = 10.77.0.1/32
```

ایران آغازکننده است و endpoint آن به TG محلی اشاره می‌کند. خارج endpoint را از بستهٔ احرازشده یاد می‌گیرد. `Table=off` ایجاد خودکار route از AllowedIPs را خاموش می‌کند؛ hookهای ایران فقط ترافیک دارای مبدأ `10.77.0.1` را به جدول اختصاصی می‌فرستند. مسیر پیش‌فرض عمومی میزبان تغییر نمی‌کند. [مرجع WireGuard](https://git.zx2c4.com/wireguard-tools/about/src/man/wg.8) و [مرجع wg-quick](https://github.com/WireGuard/wireguard-tools/blob/master/src/man/wg-quick.8)

پس از قرار دادن فایل هر نقش، روی هر دو سرور:

```sh
sudo chmod 0600 /etc/wireguard/wg-tg.conf
sudo systemctl enable --now wg-quick@wg-tg
sudo wg show wg-tg
```

سپس در ایران:

```sh
ping -c 5 10.77.0.2
ip -4 route get 1.1.1.1 from 10.77.0.1
```

مسیر دوم باید `wg-tg` را نشان دهد. مقدار MTU=1280 نقطهٔ شروع است؛ افزایش را با اندازهٔ بسته، PMTU و کیفیت واقعی مسیر بسنجید. آزمون ظرفیت اخیر MTU=1500 داشت؛ قبولی آن تنظیم برای ۱۲۸۰ یا بالعکس فرض نمی‌شود.

## ۶. خروج اینترنت و NAT در Y / خارج

نام رابط اینترنت خارج را از خروجی زیر پیدا کنید:

```sh
ip -4 route get 1.1.1.1
```

در مثال، `eth0` را با رابط واقعی عوض کنید. قواعد زیر نمونهٔ iptables هستند؛ اگر میزبان با nftables، firewalld یا UFW مدیریت می‌شود، معادل را در همان مدیر فایروال اعمال کنید. ترتیب قواعد موجود می‌تواند اثر بگذارد؛ قواعد عمومی را flush نکنید.

```sh
WAN_IF=eth0
sudo sysctl -w net.ipv4.ip_forward=1
sudo iptables -A FORWARD -i wg-tg -o "$WAN_IF" -s 10.77.0.1/32 -j ACCEPT
sudo iptables -A FORWARD -i "$WAN_IF" -o wg-tg -d 10.77.0.1/32 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
sudo iptables -t nat -A POSTROUTING -s 10.77.0.1/32 -o "$WAN_IF" -j MASQUERADE
```

این دستورات نمونه موقت‌اند؛ تکرار بی‌بررسی آن‌ها قواعد تکراری می‌سازد. پس از آزمون، forwarding را در فایل اختصاصی تنظیمات sysctl و قواعد را با مدیر فایروال همان توزیع ماندگار کنید. hookهای WireGuard ایران با سرویس رابط اجرا می‌شوند و نیازی به افزودن دوبارهٔ همان rule به‌صورت دستی ندارند.

## ۷. اتصال Xray و X-UI / 3X-UI روی X

این الگو کاربران را در Xray ایران می‌پذیرد و خروجی انتخاب‌شده را از IP داخلی WireGuard به خارج می‌فرستد. روی خارج برای این الگو Xray دوم لازم نیست؛ WireGuard و routing/NAT مقصد را فراهم می‌کنند.

یک outbound جدید به آرایهٔ موجود `outbounds` اضافه کنید:

```json
{
  "tag": "tg-wg",
  "protocol": "freedom",
  "sendThrough": "10.77.0.1",
  "settings": {},
  "streamSettings": {
    "sockopt": {
      "domainStrategy": "ForceIPv4"
    }
  }
}
```

برای inbound موردنظر، یک قاعده در `routing.rules` قرار دهید؛ `YOUR_INBOUND_TAG` را با tag واقعی عوض کنید:

```json
{
  "type": "field",
  "inboundTag": ["YOUR_INBOUND_TAG"],
  "outboundTag": "tg-wg"
}
```

این قطعه‌ها تنظیم کامل نیستند؛ تنظیم موجود را جایگزین نکنید. قاعده باید پیش از قاعدهٔ عمومیِ منطبق قرار گیرد. outbound اول در نبود تطبیق، خروجی پیش‌فرض Xray است؛ ترتیب موجود را آگاهانه حفظ کنید. `sendThrough` مبدأ خروجی را روی IP WireGuard قرار می‌دهد و policy routing مرحلهٔ ۵ مسیر آن را انتخاب می‌کند. نمونه، خروجی IPv4 را انتخاب می‌کند. [مرجع رسمی outbound](https://xtls.github.io/en/config/outbound.html) و [تنظیمات سوکت](https://xtls.github.io/en/config/transports/sockopt.html)

در X-UI/3X-UI تغییر را از بخش تنظیمات Xray همان نسخهٔ پنل اعمال کنید تا تولید مجدد config آن را حذف نکند. نام منوها نسخه‌وابسته است. پیش از restart، از تنظیم پنل نسخهٔ پشتیبان بگیرید و فایل نهایی تولیدشده را با فرمان اعتبارسنجی همان core بررسی کنید؛ سپس لاگ را ببینید و از یک کاربر آزمایشی تست بگیرید.

پورت inbound کاربران همان پورت Xray است؛ ۳۱۸۲۰ ورودی عمومی کاربر نیست. بستهٔ UDP کاربران باید توسط پروتکل inbound/outbound خود Xray نیز پشتیبانی شود. این تنظیم مسیر DNS را به‌تنهایی تعیین نمی‌کند؛ DNS و IPv6 باید جداگانه طراحی و آزموده شوند. نمونه ادعای خروج کامل همهٔ ترافیک یا جلوگیری جامع از نشت DNS ندارد.

## ۸. عبور مستقیم زیرشبکهٔ کاربران

اگر کاربران IP مسیریابی‌شده دارند، مثلاً یک زیرشبکهٔ موجود و بدون تداخل `10.88.0.0/24`، بسته‌هایشان باید پیش از رمزگذاری وارد `wg-tg` شوند. در ایران یک rule اضافی برای مبدأ همان زیرشبکه به جدول اختصاصی لازم است؛ در خارج همان زیرشبکه به AllowedIPs همتای ایران، route برگشت، forwarding و NAT اضافه می‌شود.

نمونهٔ rule اضافی **فقط در ایران** و پس از ایجاد جدول مرحلهٔ ۵:

```sh
sudo ip -4 rule add pref 17710 from 10.88.0.0/24 lookup 177
```

برای برگشت از خارج، علاوه بر `AllowedIPs = 10.77.0.1/32, 10.88.0.0/24`، به‌علت `Table=off` یک route به زیرشبکه از `wg-tg` لازم است. مثال زیر فقط پس از انتخاب واقعی این زیرشبکه و به‌عنوان بخشی از استقرار جداگانه کاربرد دارد:

```sh
sudo ip -4 route add 10.88.0.0/24 dev wg-tg
```

forwarding و اجازهٔ عبور از رابط ورودی کاربران به WireGuard در ایران نیز لازم‌اند. قواعد خارج در مرحلهٔ ۶ باید برای همین زیرشبکه گسترش یابند. مسیرها و rule اضافهٔ این بخش را جداگانه ماندگار و حذف آن‌ها را در runbook ثبت کنید. IPv6، تونل دسترسی کاربران و TProxy خودکار توسط TG ساخته نمی‌شوند.

برای چند worker، اتصال یا زیرشبکه را پیش از رمزگذاری میان WireGuardهای مستقل تقسیم کنید. بسته‌های رمز‌شدهٔ یک peer را میان ورودی نشست‌های مختلف گردشی نفرستید. [راهنمای workerها](independent-workers.md)

## ۹. پایش و عیب‌یابی

**ایران:**

```sh
systemctl status tg@edge --no-pager
journalctl -u tg@edge -n 100 --no-pager
curl --fail http://127.0.0.1:19090/healthz
curl --fail http://127.0.0.1:19090/status
sudo wg show wg-tg
```

**خارج:**

```sh
systemctl status tg@exit --no-pager
journalctl -u tg@exit -n 100 --no-pager
curl --fail http://127.0.0.1:19091/healthz
sudo wg show wg-tg
```

| نشانه | ترتیب بررسی |
|---|---|
| TG سالم نیست | آدرس، فایروال میزبان/دیتاسنتر، ساعت و گواهی، نام TLS، توکن، هماهنگی حالت و سقف بسته |
| TG سالم ولی handshake داخلی نیست | کلیدها، endpoint ایران، مقصد `app_address` خارج و پورت WireGuard |
| IP داخلی پاسخ می‌دهد ولی اینترنت نه | policy route ایران، route برگشت، forwarding، NAT، ترتیب فایروال و DNS |
| افت زیر بار | دریافت نهایی، اتلاف UDP، خطاهای سوکت کرنل، qdisc، CPU کل میزبان، RSS/PSS و بار هم‌زمان |
| پس از reboot قطع می‌شود | وضعیت دو سرویس، hookهای رابط و ماندگاری forwarding/فایروال |

`/healthz` سلامت حامل احرازشده را می‌سنجد، نه اینترنت خروجی. `/status` و `/metrics` محرمانهٔ loopback باقی بمانند. `app_output_bytes` تحویل به سوکت محلی است، نه goodput اینترنت. خطای بافر دریافت UDP ممکن است قبل از پذیرش TG رخ دهد؛ شمارندهٔ حذف داخلی صفر کافی نیست.

در آزمون پذیرش، ابتدا خط مبنای مستقیم، سپس WireGuard و TG را با تنظیم ثابت مقایسه کنید. نرخ گیرندهٔ نهایی، کف ثانیه‌ای، پروب UDP زیر بار، اتلاف، CPU کل دو میزبان و روند حافظه را ثبت کنید. قطع‌ها فقط روی حامل‌های اختصاصی و با حفظ مدیریت SSH انجام شوند. قطع هم‌زمان همهٔ مسیرها وقفه ایجاد می‌کند و باید زمان بازیابی آن جدا سنجیده شود.

## ۱۰. تغییر، بازگشت و توقف

پیش از تغییر، commit باینری و تنظیمات سالم را در محل خصوصی ثبت کنید. برای تغییر نوع حامل یا حالت بسته‌ای، دو سر باید هماهنگ به‌روز شوند. پس از تغییر، `check` و لاگ راه‌اندازی را بررسی و سرویس نقش مربوط را restart کنید؛ تغییر config بدون restart اعمال نمی‌شود.

توقف نمونهٔ ایران:

```sh
sudo systemctl disable --now wg-quick@wg-tg
sudo systemctl disable --now tg@edge
```

در خارج نام TG برابر `tg@exit` است. پایین‌آوردن WireGuard ایران، hookهای حذف rule/route همان رابط را اجرا می‌کند. قواعد اضافی بخش ۸ و NAT/فایروال باید با مشخصات دقیق قواعد خودتان برداشته شوند؛ forwarding سراسری را اگر سرویس دیگری نیاز دارد خاموش نکنید. کلیدها و تنظیمات برای بازگشت نگه‌داری شوند؛ حذف بازگشتی پوشه‌ها یا پاک‌کردن عمومی فایروال بخشی از این راهنما نیست.

## ۱۱. دامنهٔ کنترل این راهنما

در ۲۰۲۶/۱۰/۲۴ کنترل‌های زیر انجام شدند:

- ۷۱ ارجاع محلی در دو README و این راهنما معتبر بودند؛ فهرست و بلوک‌های کد ساختار معتبر داشتند.
- نحو ۲۲ بلوک shell و چهار hook رابط با `bash -n` بررسی شد؛ دو قطعهٔ JSON پنل نیز parse شدند. بررسی نحو، اجرای موفق شبکه را ثابت نمی‌کند.
- با باینری ویندوز همان runtime برابر `e9feb4f`، دستور تولید جفت، کد Python همین متن و `check` هر دو نقش اجرا شدند. انتقال تفکیک‌شدهٔ هویت‌ها بدون کلید CA و بدون کلید نقش مقابل نیز بررسی شد.
- در آزمون دود محلی، هر دو حامل CONNECT و QUIC-stream در هر دو نقش سالم شدند؛ ۶۴ پیام UDP در اندازه‌های ۶۴، ۱۲۸، ۱۴۰۰ و ۲۰۴۸ بایت با محتوای صحیح برگشتند. اعتبارنامه‌های موقت حذف و پردازش‌های آزمون متوقف شدند.

این کنترل، آزمون سرعت یا قطع مسیر تازه نبود. Docker در زمان بررسی در دسترس نبود؛ ساخت و نصب تازهٔ لینوکس، اجرای systemd، WireGuard، NAT، پنل و reboot دو سرور واقعی در این کنترل تأیید نشدند. هدف جاری کیفیت حدود یک گیگابیت و هدف نهایی ۱۰ گیگابیت همچنان نیازمند آزمون پذیرش‌اند. [گزارش ظرفیت فعلی](one-gig-validation.md)
