"""本地上游 stub：用确定性的离线数据模拟六个第三方数据源。

E2E 与 k6 压测共用这台 stub——被测 Go 服务的全部上游环境变量
（MOXFIELD_API_URL / COMMANDERSALT_API_URL / SCRYFALL_API_URL /
SPELLBOOK_API_URL / EDHREC_JSON_URL / GETCARDS_URL / SCRYFALL_IMAGE_URL）
指向它之后，整套分析流水线不再触达任何真实第三方。

能力：
- 按路径模拟六源的真实响应形状（与各 provider 的解析代码一一对应）；
- 对未知卡名合成合法数据（getcards / Scryfall collection 收到任何
  名字都能答），因此压测可以随意生成新牌表强制冷启动；
- 控制面：GET /__stats 查各源计数、POST /__reset 清零、
  POST /__configure 注入按源延迟 / 错误状态 / 挂起（故障演练）。
"""

from __future__ import annotations

import json
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# --------------------------------------------------------------------------
# 语料：一付已知的 Sram 白色装备牌组 + 组牌相关扩展卡池。
# 所有源都从这一份卡表派生，保证跨源数据一致（同名卡在 Scryfall、
# getcards、CommanderSalt 三处的张数/费用必须对得上）。
# --------------------------------------------------------------------------

CANONICAL_DECK_ID = "stubdeck01"
CANONICAL_DECK_URL = "https://www.moxfield.com/decks/stubdeck01"
CANONICAL_DECK_NAME = "Sram Equipment (stub)"

COMMANDER = "Sram, Senior Edificer"

# name -> (数量, 是否主将)
DECK_CARDS: dict[str, tuple[int, bool]] = {
    COMMANDER: (1, True),
    "Puresteel Paladin": (1, False),
    "Swiftfoot Boots": (1, False),
    "Sword of the Animist": (1, False),
    "Sol Ring": (1, False),
    "Weathered Wayfarer": (1, False),
    "Land Tax": (1, False),
    "Mentor of the Meek": (1, False),
    "Swords to Plowshares": (1, False),
    "Generous Gift": (1, False),
    "Wrath of God": (1, False),
    "Plains": (36, False),
}

# 卡片元数据：Scryfall / getcards / CommanderSalt 三源共用。
# salt 是 CommanderSalt 的单卡咸度；rank 是 getcards 的 edhrec_rank。
CARD_INFO: dict[str, dict] = {
    COMMANDER: dict(cmc=2, mana_cost="{W}{W}", type_line="Legendary Creature — Dwarf Advisor",
                    oracle="Whenever you cast an Aura, Equipment, or Vehicle spell, draw a card.",
                    colors="W", produced=["W"], price=0.35, rank=318, salt=0.0, game_changer=False),
    "Puresteel Paladin": dict(cmc=2, mana_cost="{W}{W}", type_line="Creature — Human Knight",
                              oracle="Whenever an Equipment enters the battlefield under your control, draw a card. Equip abilities you activate cost {1} less.",
                              colors="W", produced=[], price=0.25, rank=512, salt=0.6, game_changer=False),
    "Swiftfoot Boots": dict(cmc=2, mana_cost="{2}", type_line="Artifact — Equipment",
                            oracle="Equipped creature has hexproof and haste. Equip {1}.",
                            colors="", produced=[], price=0.12, rank=89, salt=0.2, game_changer=False),
    "Sword of the Animist": dict(cmc=2, mana_cost="{2}", type_line="Artifact — Equipment",
                                 oracle="Whenever equipped creature attacks, put a basic land card from your library onto the battlefield tapped. Equip {2}.",
                                 colors="", produced=[], price=1.20, rank=150, salt=0.4, game_changer=False),
    "Sol Ring": dict(cmc=1, mana_cost="{1}", type_line="Artifact",
                     oracle="{T}: Add {C}{C}.",
                     colors="", produced=["C"], price=0.49, rank=1, salt=1.1, game_changer=False),
    "Weathered Wayfarer": dict(cmc=1, mana_cost="{W}", type_line="Creature — Human Cleric",
                               oracle="{W}, {T}: Search your library for a land card, reveal it, put it into your hand, then shuffle.",
                               colors="W", produced=[], price=0.18, rank=410, salt=0.3, game_changer=False),
    "Land Tax": dict(cmc=1, mana_cost="{W}", type_line="Enchantment",
                     oracle="At the beginning of your upkeep, search your library for up to three basic land cards and put them into your hand.",
                     colors="W", produced=[], price=2.10, rank=226, salt=0.5, game_changer=False),
    "Mentor of the Meek": dict(cmc=3, mana_cost="{2}{W}", type_line="Creature — Human Soldier",
                               oracle="Whenever another creature enters the battlefield under your control, you may pay {1}. If you do, draw a card.",
                               colors="W", produced=[], price=0.22, rank=333, salt=0.4, game_changer=False),
    "Swords to Plowshares": dict(cmc=1, mana_cost="{W}", type_line="Instant",
                                 oracle="Exile target creature. Its controller gains life equal to its power.",
                                 colors="W", produced=[], price=0.89, rank=14, salt=0.3, game_changer=False),
    "Generous Gift": dict(cmc=3, mana_cost="{2}{W}", type_line="Instant",
                          oracle="Destroy target permanent. Its controller creates a 3/3 green Elephant creature token.",
                          colors="W", produced=[], price=0.30, rank=95, salt=0.3, game_changer=False),
    "Wrath of God": dict(cmc=4, mana_cost="{2}{W}{W}", type_line="Sorcery",
                         oracle="Destroy all creatures. They can't be regenerated.",
                         colors="W", produced=[], price=1.85, rank=41, salt=0.9, game_changer=False),
    "Plains": dict(cmc=0, mana_cost="", type_line="Basic Land — Plains",
                   oracle="{T}: Add {W}.",
                   colors="W", produced=["W"], price=0.01, rank=0, salt=0.0, game_changer=False),
    # 组牌 / 推荐用扩展卡池（不在初始牌组里）
    "Smothering Tithe": dict(cmc=3, mana_cost="{2}{W}", type_line="Enchantment",
                             oracle="Whenever an opponent draws a card, that player may pay {2}. If they don't, you create a Treasure token.",
                             colors="W", produced=[], price=4.20, rank=60, salt=1.6, game_changer=True),
    "Ghostly Prison": dict(cmc=3, mana_cost="{2}{W}", type_line="Enchantment",
                           oracle="Creatures can't attack you or planeswalkers you control unless their controller pays {2} for each of those creatures.",
                           colors="W", produced=[], price=0.80, rank=190, salt=0.7, game_changer=False),
    "Path to Exile": dict(cmc=1, mana_cost="{W}", type_line="Instant",
                          oracle="Exile target creature. Its controller may search their library for a basic land card and put it onto the battlefield tapped.",
                          colors="W", produced=[], price=1.10, rank=17, salt=0.3, game_changer=False),
    "Mask of Memory": dict(cmc=2, mana_cost="{2}", type_line="Artifact — Equipment",
                           oracle="Whenever equipped creature deals combat damage to a player, you may draw two cards, then discard a card. Equip {1}.",
                           colors="", produced=[], price=0.15, rank=280, salt=0.3, game_changer=False),
    "Keeper of the Accord": dict(cmc=4, mana_cost="{3}{W}", type_line="Creature — Human Soldier",
                                 oracle="At the beginning of each opponent's upkeep, create a 1/1 white Soldier token or draw a card.",
                                 colors="W", produced=[], price=0.40, rank=350, salt=0.4, game_changer=False),
    "Ria Ivor, Bane of Bladehold": dict(cmc=4, mana_cost="{2}{W}{B}", type_line="Legendary Creature — Phyrexian Knight",
                                        oracle="Whenever Ria Ivor attacks, put a +1/+1 counter on it. It gains trample if you control another Phyrexian.",
                                        colors="WB", produced=["W", "B"], price=0.60, rank=450, salt=0.5, game_changer=False),
}

# EDHREC 推荐池：commander slug -> cardlists（形状对应 edhrec.page）
EDHREC_PAGE_SLUG = "sram-senior-edificer"


def _edhrec_recommendation(name: str, synergy: float, decks: int, potential: int) -> dict:
    return {
        "name": name,
        "url": "/cards/" + urllib.parse.quote(name.lower().replace(",", "").replace("  ", " ").replace(" ", "-")) + "/",
        "synergy": synergy,
        "num_decks": decks,
        "potential_decks": potential,
        "is_partner": False,
    }


EDHREC_CARDLISTS = [
    {
        "header": "Top Cards",
        "tag": "top",
        "cardviews": [
            _edhrec_recommendation("Smothering Tithe", 0.41, 5200, 6100),
            _edhrec_recommendation("Puresteel Paladin", 0.38, 4800, 6100),
            _edhrec_recommendation("Mask of Memory", 0.35, 2100, 6100),
        ],
    },
    {
        "header": "Card Advantage",
        "tag": "draw",
        "cardviews": [
            _edhrec_recommendation("Mentor of the Meek", 0.29, 3900, 6100),
            _edhrec_recommendation("Keeper of the Accord", 0.22, 1500, 6100),
        ],
    },
    {
        "header": "Removal",
        "tag": "removal",
        "cardviews": [
            _edhrec_recommendation("Path to Exile", 0.18, 5600, 6100),
            _edhrec_recommendation("Generous Gift", 0.16, 4100, 6100),
        ],
    },
    {
        "header": "Utility",
        "tag": "utility",
        "cardviews": [
            _edhrec_recommendation("Ghostly Prison", 0.24, 2600, 6100),
            _edhrec_recommendation("Sword of the Animist", 0.31, 3300, 6100),
        ],
    },
]

EDHREC_TAG_COUNTS = [
    {"slug": "equipment", "value": "Equipment", "count": 14},
    {"slug": "aura", "value": "Auras", "count": 9},
    {"slug": "vehicle", "value": "Vehicles", "count": 4},
]

# Spellbook 组合：只回这两张名下的一条 2 卡组合（客户端发来的是小写名）
SPELLBOOK_COMBO_CARDS = ("Mentor of the Meek", "Sol Ring")
_SPELLBOOK_COMBO_KEYS = {name.lower() for name in SPELLBOOK_COMBO_CARDS}
SPELLBOOK_COMBO = {
    "id": "stub-0001",
    "description": "Synthetic stub combo: pay mana to draw repeatedly.",
    "manaValueNeeded": 5,
    "bracketTag": "value",
    "status": "OK",
    "uses": [
        {"card": {"name": SPELLBOOK_COMBO_CARDS[0], "oracleId": "stub-oracle-0001",
                  "imageUriFrontNormal": "/img/normal/stub-0001.jpg",
                  "imageUriFrontSmall": "/img/small/stub-0001.jpg"}, "zoneLocations": ["battlefield"]},
        {"card": {"name": SPELLBOOK_COMBO_CARDS[1], "oracleId": "stub-oracle-0002",
                  "imageUriFrontNormal": "/img/normal/stub-0002.jpg",
                  "imageUriFrontSmall": "/img/small/stub-0002.jpg"}, "zoneLocations": ["battlefield"]},
    ],
    "produces": [{"feature": {"id": 1, "name": "Infinite draw"}},
                 {"feature": {"id": 2, "name": "Synthetic feature"}}],
    "requires": [{"quantity": 1, "template": {"id": 10}}, {"quantity": 1, "template": {"id": 11}}],
}

# CommanderSalt 顶层评分（形状对应 commandersalt.response）
SALT_RATINGS = dict(
    powerLevelRating=5.4,
    bracketRating=3.0,
    saltRating=12.3,
    comboRating=1.2,
    synergyRating=0.8,
    threatRating=2.6,
    brackets={"csBracket": 3, "wotcBracket": 2, "displayBracket": 3},
    suggestions=[
        {"name": "Smothering Tithe", "reason": "缺加速：产钱机动强", "gap": "ramp"},
        {"name": "Path to Exile", "reason": "单体干扰偏少", "gap": "single_interaction"},
    ],
)

# 1x1 灰色 PNG（图片代理路径用，避免加载真实卡图）
TINY_PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d49484452000000010000000108020000009077"
    "3df80000000c4944415408d763f8cfc00000030101"
    "00c9fe92ef0000000049454e44ae426082"
)


def _card_info(name: str) -> dict:
    """已知卡返回语料，未知卡按通用规则合成——压测可随意发明新卡名。"""
    if name in CARD_INFO:
        return CARD_INFO[name]
    return dict(cmc=2, mana_cost="{2}", type_line="Creature — Stub",
                oracle=f"Synthetic card generated for {name}. Draw a card.",
                colors="W", produced=[], price=0.05, rank=900, salt=0.2, game_changer=False)


def _color_identity(colors: str) -> list[str]:
    return list(dict.fromkeys(colors.upper()))


def _scryfall_row(name: str) -> dict:
    info = _card_info(name)
    slug = urllib.parse.quote(name.lower().replace(",", "").replace("  ", " ").replace(" ", "-"))
    return {
        "object": "card",
        "oracle_id": "stub-oracle-" + urllib.parse.quote(name.lower()),
        "name": name,
        "printed_name": name,
        "mana_cost": info["mana_cost"],
        "cmc": info["cmc"],
        "type_line": info["type_line"],
        "oracle_text": info["oracle"],
        "colors": list(info["colors"]),
        "color_identity": _color_identity(info["colors"]),
        "keywords": [],
        "legalities": {"commander": "legal", "vintage": "legal"},
        "image_uris": {"normal": f"/img/normal/{slug}.jpg", "small": f"/img/small/{slug}.jpg"},
        "layout": "normal",
        "produced_mana": info["produced"],
        "power": "2",
        "game_changer": info["game_changer"],
    }


def _getcards_row(name: str) -> dict:
    info = _card_info(name)
    return {
        "name": name,
        "flavor_name": None,
        "price": info["price"],
        "cmc": info["cmc"],
        "edhrec_rank": info["rank"],
        "colors": list(info["colors"]),
        "produced_mana": info["produced"],
        "mana_cost": info["mana_cost"],
        "type_line": info["type_line"],
        "layout": "normal",
        "reserved": False,
        "legal": "legal",
        "gamechanger": info["game_changer"],
        "oracle_text": [info["oracle"]],
    }


def _moxfield_deck(deck_id: str) -> dict:
    boards: dict[str, dict] = {"commanders": {"cards": {}}, "mainboard": {"cards": {}}}
    for name, (quantity, commander) in DECK_CARDS.items():
        zone = "commanders" if commander else "mainboard"
        boards[zone]["cards"][f"stub-{urllib.parse.quote(name.lower())}"] = {
            "quantity": quantity,
            "card": {"name": name},
        }
    return {"id": deck_id, "name": CANONICAL_DECK_NAME, "boards": boards}


def _salt_deck() -> dict:
    cards = {}
    for name, (quantity, commander) in DECK_CARDS.items():
        cards[name.lower()] = {
            "name": name,
            "count": quantity,
            "salt": str(_card_info(name)["salt"]),
            "isCommander": commander,
            "isFrontFace": True,
            "price": {"usd": str(_card_info(name)["price"])},
            "types": _card_info(name)["type_line"],
        }
    return {
        "id": CANONICAL_DECK_ID,
        "name": None,
        "title": CANONICAL_DECK_NAME,
        "deckName": None,
        "commanders": [COMMANDER],
        "cards": cards,
        "_cardCount": sum(q for q, _ in DECK_CARDS.values()),
        **SALT_RATINGS,
    }


def _edhrec_page() -> dict:
    return {
        "tag_counts": EDHREC_TAG_COUNTS,
        "container": {"json_dict": {"cardlists": EDHREC_CARDLISTS}},
    }


def _edhrec_rankings() -> dict:
    return {
        "container": {"json_dict": {"cardlists": [{
            "header": "Top Commanders",
            "tag": "top",
            "cardviews": [
                {"name": COMMANDER, "url": "/commanders/sram-senior-edificer/",
                 "synergy": 0.0, "num_decks": 6100, "potential_decks": 6100, "is_partner": False},
                {"name": "Ria Ivor, Bane of Bladehold", "url": "/commanders/ria-ivor-bane-of-bladehold/",
                 "synergy": 0.0, "num_decks": 1200, "potential_decks": 1200, "is_partner": False},
            ],
        }]}},
    }


# --------------------------------------------------------------------------
# HTTP stub 本体
# --------------------------------------------------------------------------

# 源键（/__stats 与 /__configure 的粒度）
SOURCE_KEYS = [
    "moxfield", "salt", "scryfall_collection", "scryfall_search",
    "scryfall_autocomplete", "spellbook", "edhrec", "edhrec_rankings",
    "getcards", "images",
]


class StubState:
    """请求计数 + 故障注入配置（/__stats、/__reset、/__configure 的后端）。"""

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.counts = {key: 0 for key in SOURCE_KEYS}
        # source -> {"delay_ms": int, "status": int, "hang": bool}
        self.behavior: dict[str, dict] = {}

    def reset(self) -> None:
        with self.lock:
            self.counts = {key: 0 for key in SOURCE_KEYS}
            self.behavior = {}

    def hit(self, source: str) -> None:
        with self.lock:
            self.counts[source] = self.counts.get(source, 0) + 1

    def snapshot(self) -> dict:
        with self.lock:
            return dict(self.counts)

    def configure(self, behavior: dict) -> None:
        with self.lock:
            self.behavior = {str(k): dict(v) for k, v in behavior.items()}

    def effect(self, source: str) -> dict:
        with self.lock:
            return dict(self.behavior.get(source, {}))


STATE = StubState()


def _classify(path: str, headers) -> tuple[str, str | None]:
    """把请求映射到 (源键, 处理函数名)；返回 (source, None) 表示未识别。"""
    parsed = urllib.parse.urlparse(path)
    route = parsed.path
    if route.startswith("/v3/decks/all/"):
        return "moxfield", "moxfield"
    if route == "/decks":
        return "salt", "salt"
    if route == "/cards/collection":
        return "scryfall_collection", "scryfall_collection"
    if route == "/cards/search":
        return "scryfall_search", "scryfall_search"
    if route == "/cards/autocomplete":
        return "scryfall_autocomplete", "scryfall_autocomplete"
    if route == "/variants/":
        return "spellbook", "spellbook"
    if route == "/pages/commanders/year.json":
        return "edhrec_rankings", "edhrec_rankings"
    if route.startswith("/pages/commanders/") and route.endswith(".json"):
        return "edhrec", "edhrec"
    if headers.get("card-list"):
        return "getcards", "getcards"
    if route.startswith("/img/") or route.endswith((".jpg", ".png", ".webp")):
        return "images", "images"
    return "unknown", None


class StubHTTPServer(ThreadingHTTPServer):
    """吞掉 keep-alive 被客户端重置/中止的噪声（Go 客户端关空闲或超时
    取消的连接在 Windows 上表现为 RST / 中止）。"""

    daemon_threads = True

    def handle_error(self, request, client_address):
        import sys as _sys

        if issubclass(_sys.exc_info()[0], (ConnectionResetError, ConnectionAbortedError, BrokenPipeError)):
            return
        super().handle_error(request, client_address)


class StubHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "UpstreamStub/1.0"

    def log_message(self, fmt, *args):  # 静音访问日志（压测时刷屏）
        pass

    # ---- 响应工具 -------------------------------------------------------

    def _send_json(self, payload: dict, status: int = 200) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_bytes(self, body: bytes, content_type: str, status: int = 200) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _apply_behavior(self, source: str) -> bool:
        """执行故障注入；返回 False 表示请求已被注入的 status 直接应答。"""
        effect = STATE.effect(source)
        if effect.get("hang"):
            time.sleep(min(effect.get("hang_seconds", 30), 120))
        delay = effect.get("delay_ms", 0)
        if delay:
            time.sleep(delay / 1000.0)
        status = effect.get("status")
        if status:
            self._send_json({"error": f"injected status {status} for {source}"}, status=int(status))
            return False
        return True

    # ---- 路由 -----------------------------------------------------------

    def do_GET(self) -> None:  # noqa: N802
        source, route = _classify(self.path, self.headers)
        if route is None and source == "unknown":
            if self.path.startswith("/__stats"):
                self._send_json({"counts": STATE.snapshot()})
                return
            self._send_json({"error": f"no stub route for {self.path}"}, status=404)
            return
        STATE.hit(source)
        if not self._apply_behavior(source):
            return
        query = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
        if route == "moxfield":
            deck_id = self.path.rsplit("/", 1)[-1].split("?")[0]
            self._send_json(_moxfield_deck(deck_id))
        elif route == "scryfall_autocomplete":
            prefix = query.get("q", [""])[0].lower()
            names = [n for n in CARD_INFO if n.lower().startswith(prefix)][:10]
            self._send_json({"object": "catalog", "total_values": len(names), "data": names})
        elif route == "scryfall_search":
            self._send_json({"object": "list", "has_more": False, "data": [
                _scryfall_row(name) for name in ["Ghostly Prison", "Mask of Memory", "Keeper of the Accord"]
            ]})
        elif route == "spellbook":
            name = query.get("q", [""])[0].strip().lower()
            results = [SPELLBOOK_COMBO] if name in _SPELLBOOK_COMBO_KEYS else []
            self._send_json({"results": results, "count": len(results)})
        elif route == "edhrec":
            self._send_json(_edhrec_page())
        elif route == "edhrec_rankings":
            self._send_json(_edhrec_rankings())
        elif route == "getcards":
            names = [urllib.parse.unquote(part) for part in self.headers.get("card-list", "").split("~") if part]
            self._send_json({"data": [_getcards_row(n) for n in names]})
        elif route == "images":
            self._send_bytes(TINY_PNG, "image/png")
        else:
            self._send_json({"error": f"unhandled GET route {route}"}, status=404)

    def do_POST(self) -> None:  # noqa: N802
        if self.path.split("?")[0] == "/__reset":
            STATE.reset()
            self._send_json({"ok": True})
            return
        if self.path.split("?")[0] == "/__configure":
            length = int(self.headers.get("Content-Length", 0))
            STATE.configure(json.loads(self.rfile.read(length) or b"{}"))
            self._send_json({"ok": True})
            return

        source, route = _classify(self.path, self.headers)
        if route is None:
            self._send_json({"error": f"no stub route for {self.path}"}, status=404)
            return
        STATE.hit(source)
        if not self._apply_behavior(source):
            return
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        if route == "salt":
            self._send_json(_salt_deck())
        elif route == "scryfall_collection":
            payload = json.loads(body or b"{}")
            names = [item.get("name", "") for item in payload.get("identifiers", [])]
            self._send_json({"data": [_scryfall_row(n) for n in names if n], "not_found": []})
        else:
            self._send_json({"error": f"unhandled POST route {route}"}, status=404)


def start_stub(host: str = "127.0.0.1", port: int = 0) -> tuple[StubHTTPServer, str]:
    """启动 stub 并返回 (server, base_url)。线程化，daemon 线程随进程退出。"""
    server = StubHTTPServer((host, port), StubHandler)
    threading.Thread(target=server.serve_forever, daemon=True, name="upstream-stub").start()
    return server, f"http://{host}:{server.server_address[1]}"


def start_stub_pool(count: int, host: str = "127.0.0.1") -> tuple[list[StubHTTPServer], list[str]]:
    """启动 count 个共享同一份计数/故障配置的 stub 实例。

    被测 Go 客户端对每个 host 限 MaxConnsPerHost=10（对真实上游的防突发
    设计）；若六源全指向同一 stub 端口，压测并发会先撞这个连接池上限，
    测出的是客户端排队而不是服务编排。一源一端口更贴近真实拓扑。
    """
    servers: list[StubHTTPServer] = []
    bases: list[str] = []
    for _ in range(count):
        server, base = start_stub(host)
        servers.append(server)
        bases.append(base)
    return servers, bases


if __name__ == "__main__":
    # python tests/upstream_stub.py [port] —— 手动起 stub 供调试
    import sys

    listen_port = int(sys.argv[1]) if len(sys.argv) > 1 else 18799
    server, base = start_stub(port=listen_port)
    print(f"upstream stub listening on {base} (stats: {base}/__stats)", flush=True)
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        server.shutdown()
