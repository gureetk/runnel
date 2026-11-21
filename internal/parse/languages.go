// SPDX-License-Identifier: GPL-3.0-or-later

package parse

// The language codes of ISO 639-2, as the iso-codes project lists them in
// iso_639-2.json, version 4.16.0: the codes only. A name gives a language
// in two letters (ISO 639-1) or in three (ISO 639-2); it is kept in three,
// the bibliographic code where there are two, as Matroska keeps it.

// twoLetter maps an ISO 639-1 code to its ISO 639-2 code.
var twoLetter = map[string]string{
	"aa": "aar", "ab": "abk", "ae": "ave", "af": "afr", "ak": "aka", "am": "amh", "an": "arg", "ar": "ara",
	"as": "asm", "av": "ava", "ay": "aym", "az": "aze", "ba": "bak", "be": "bel", "bg": "bul", "bh": "bih",
	"bi": "bis", "bm": "bam", "bn": "ben", "bo": "tib", "br": "bre", "bs": "bos", "ca": "cat", "ce": "che",
	"ch": "cha", "co": "cos", "cr": "cre", "cs": "cze", "cu": "chu", "cv": "chv", "cy": "wel", "da": "dan",
	"de": "ger", "dv": "div", "dz": "dzo", "ee": "ewe", "el": "gre", "en": "eng", "eo": "epo", "es": "spa",
	"et": "est", "eu": "baq", "fa": "per", "ff": "ful", "fi": "fin", "fj": "fij", "fo": "fao", "fr": "fre",
	"fy": "fry", "ga": "gle", "gd": "gla", "gl": "glg", "gn": "grn", "gu": "guj", "gv": "glv", "ha": "hau",
	"he": "heb", "hi": "hin", "ho": "hmo", "hr": "hrv", "ht": "hat", "hu": "hun", "hy": "arm", "hz": "her",
	"ia": "ina", "id": "ind", "ie": "ile", "ig": "ibo", "ii": "iii", "ik": "ipk", "io": "ido", "is": "ice",
	"it": "ita", "iu": "iku", "ja": "jpn", "jv": "jav", "ka": "geo", "kg": "kon", "ki": "kik", "kj": "kua",
	"kk": "kaz", "kl": "kal", "km": "khm", "kn": "kan", "ko": "kor", "kr": "kau", "ks": "kas", "ku": "kur",
	"kv": "kom", "kw": "cor", "ky": "kir", "la": "lat", "lb": "ltz", "lg": "lug", "li": "lim", "ln": "lin",
	"lo": "lao", "lt": "lit", "lu": "lub", "lv": "lav", "mg": "mlg", "mh": "mah", "mi": "mao", "mk": "mac",
	"ml": "mal", "mn": "mon", "mr": "mar", "ms": "may", "mt": "mlt", "my": "bur", "na": "nau", "nb": "nob",
	"nd": "nde", "ne": "nep", "ng": "ndo", "nl": "dut", "nn": "nno", "no": "nor", "nr": "nbl", "nv": "nav",
	"ny": "nya", "oc": "oci", "oj": "oji", "om": "orm", "or": "ori", "os": "oss", "pa": "pan", "pi": "pli",
	"pl": "pol", "ps": "pus", "pt": "por", "qu": "que", "rm": "roh", "rn": "run", "ro": "rum", "ru": "rus",
	"rw": "kin", "sa": "san", "sc": "srd", "sd": "snd", "se": "sme", "sg": "sag", "si": "sin", "sk": "slo",
	"sl": "slv", "sm": "smo", "sn": "sna", "so": "som", "sq": "alb", "sr": "srp", "ss": "ssw", "st": "sot",
	"su": "sun", "sv": "swe", "sw": "swa", "ta": "tam", "te": "tel", "tg": "tgk", "th": "tha", "ti": "tir",
	"tk": "tuk", "tl": "tgl", "tn": "tsn", "to": "ton", "tr": "tur", "ts": "tso", "tt": "tat", "tw": "twi",
	"ty": "tah", "ug": "uig", "uk": "ukr", "ur": "urd", "uz": "uzb", "ve": "ven", "vi": "vie", "vo": "vol",
	"wa": "wln", "wo": "wol", "xh": "xho", "yi": "yid", "yo": "yor", "za": "zha", "zh": "chi", "zu": "zul",
}

// terminology maps the 20 terminology codes of ISO 639-2 to their
// bibliographic codes.
var terminology = map[string]string{
	"bod": "tib", "ces": "cze", "cym": "wel", "deu": "ger", "ell": "gre", "eus": "baq",
	"fas": "per", "fra": "fre", "hye": "arm", "isl": "ice", "kat": "geo", "mkd": "mac",
	"mri": "mao", "msa": "may", "mya": "bur", "nld": "dut", "ron": "rum", "slk": "slo",
	"sqi": "alb", "zho": "chi",
}

// threeLetter holds every code of ISO 639-2, of both kinds.
var threeLetter = setOf(`
aar abk ace ach ada ady afa afh afr ain aka akk alb ale alg alt amh ang anp apa
ara arc arg arm arn arp art arw asm ast ath aus ava ave awa aym aze bad bai bak
bal bam ban baq bas bat bej bel bem ben ber bho bih bik bin bis bla bnt bod bos
bra bre btk bua bug bul bur byn cad cai car cat cau ceb cel ces cha chb che chg
chi chk chm chn cho chp chr chu chv chy cmc cnr cop cor cos cpe cpf cpp cre crh
crp csb cus cym cze dak dan dar day del den deu dgr din div doi dra dsb dua dum
dut dyu dzo efi egy eka ell elx eng enm epo est eus ewe ewo fan fao fas fat fij
fil fin fiu fon fra fre frm fro frr frs fry ful fur gaa gay gba gem geo ger gez
gil gla gle glg glv gmh goh gon gor got grb grc gre grn gsw guj gwi hai hat hau
haw heb her hil him hin hit hmn hmo hrv hsb hun hup hye iba ibo ice ido iii ijo
iku ile ilo ina inc ind ine inh ipk ira iro isl ita jav jbo jpn jpr jrb kaa kab
kac kal kam kan kar kas kat kau kaw kaz kbd kha khi khm kho kik kin kir kmb kok
kom kon kor kos kpe krc krl kro kru kua kum kur kut lad lah lam lao lat lav lez
lim lin lit lol loz ltz lua lub lug lui lun luo lus mac mad mag mah mai mak mal
man mao map mar mas may mdf mdr men mga mic min mis mkd mkh mlg mlt mnc mni mno
moh mon mos mri msa mul mun mus mwl mwr mya myn myv nah nai nap nau nav nbl nde
ndo nds nep new nia nic niu nld nno nob nog non nor nqo nso nub nwc nya nym nyn
nyo nzi oci oji ori orm osa oss ota oto paa pag pal pam pan pap pau peo per phi
phn pli pol pon por pra pro pus que raj rap rar roa roh rom ron rum run rup rus
sad sag sah sai sal sam san sas sat scn sco sel sem sga sgn shn sid sin sio sit
sla slk slo slv sma sme smi smj smn smo sms sna snd snk sog som son sot spa sqi
srd srn srp srr ssa ssw suk sun sus sux swa swe syc syr tah tai tam tat tel tem
ter tet tgk tgl tha tib tig tir tiv tkl tlh tli tmh tog ton tpi tsi tsn tso tuk
tum tup tur tut tvl twi tyv udm uga uig ukr umb und urd uzb vai ven vie vol vot
wak wal war was wel wen wln wol xal xho yao yap yid yor ypk zap zbl zen zgh zha
zho znd zul zun zxx zza
`)
