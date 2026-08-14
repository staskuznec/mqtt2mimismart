/*
 * Вкладка «MQTT» в панели MimiSetup.
 *
 * Панель собрана Sencha Cmd в один минифицированный файл, и править его нельзя:
 * обновление панели снесёт правку, а найти её потом в простыне на два мегабайта
 * невозможно. Поэтому подключаемся снаружи, отдельным файлом.
 *
 * Опереться есть на что: разделы панели — это вкладки контейнера с постоянным
 * идентификатором centerPanel (см. appOnBeforeRender в app.js). Добавить туда
 * свою вкладку — обычный вызов add, никакого вмешательства во внутренности.
 *
 * Сложность в том, что панель запоминает состояние вкладок и при загрузке
 * пересобирает их заново: applyState делает removeAll и восстанавливает список
 * по сохранённым идентификаторам. Нашей вкладки в этот момент ещё нет — её
 * добавляет этот скрипт, и происходит это позже, — поэтому она выпадает.
 * А активная вкладка запоминается НОМЕРОМ, и после пропажи номер указывает уже
 * на другой раздел: отсюда и «перегрузил страницу — попал не туда».
 *
 * Чужой applyState переписывать нельзя, поэтому вкладка держится сама:
 * возвращается на своё место, если её снесли, и помнит, была ли она открыта.
 *
 * Отдельная история — порядок разделов. Панель хранит его в cookie, картой
 * «идентификатор вкладки → место», и подчищать эту карту приходится нам:
 * подробности у dropTab и remember.
 *
 * Подключается строкой в index.php панели, после app.js:
 *   <script src="mqtt-tab.js"></script>
 */
(function () {
    "use strict";

    // Адрес шлюза. Подставляется установщиком: за прокси это подкаталог того же
    // сервера, без прокси — прямой порт.
    var GATEWAY_URL = "/mqtt/";

    var TAB_ID = "mqtt2mimismart";
    var TAB_TITLE = "MQTT";
    var STORE_KEY = "mqtt2mimismart.tab";

    // Панель строится не сразу: centerPanel появляется только после входа, а до
    // этого на экране форма авторизации.
    var CHECK_EVERY = 500;

    // Проверка не прекращается: панель может пересобрать вкладки в любой момент
    // — при восстановлении состояния или после повторного входа. Раз в
    // несколько секунд это ничего не стоит, а вкладка перестаёт пропадать.
    var WATCH_EVERY = 3000;

    var restored = false; // открывали ли мы вкладку по сохранённому признаку

    function loadState() {
        try {
            return JSON.parse(window.localStorage.getItem(STORE_KEY)) || {};
        } catch (e) {
            return {};
        }
    }

    function saveState(state) {
        try {
            window.localStorage.setItem(STORE_KEY, JSON.stringify(state));
        } catch (e) {
            // Приватный режим или переполненное хранилище: вкладка всё равно
            // работает, просто не помнит своего места.
        }
    }

    function tabConfig() {
        return {
            id: TAB_ID,
            title: TAB_TITLE,
            xtype: "panel",
            layout: "fit",
            closable: false,
            // Шлюз показывается целиком, своим интерфейсом: повторять его
            // страницы на ExtJS значило бы вести две версии одного и того же.
            html:
                '<iframe src="' + GATEWAY_URL + '" ' +
                'style="border:0;width:100%;height:100%;display:block" ' +
                'title="Шлюз MQTT"></iframe>'
        };
    }

    // place возвращает вкладку на своё место — туда, куда её перетащили.
    function place(center) {
        var state = loadState();
        var index = state.index;

        var tab;
        if (typeof index === "number" && index >= 0 && index < center.items.getCount()) {
            tab = center.insert(index, tabConfig());
        } else {
            tab = center.add(tabConfig());
        }

        // Открываем только один раз за загрузку страницы: дальше выбор за
        // человеком, и перебивать его нельзя.
        if (!restored && state.active) {
            restored = true;
            center.setActiveTab(tab);
        }
        return tab;
    }

    // remember запоминает, где вкладка стоит и открыта ли она.
    function remember(center) {
        var tab = Ext.getCmp(TAB_ID);
        if (!tab) {
            return;
        }
        var index = center.items.indexOf(tab);
        if (index < 0) {
            // Вкладка уже вынута из панели: места у неё сейчас нет, и записать
            // «-1» значило бы потерять запомненное — при следующей загрузке
            // вкладка встала бы в конец списка.
            return;
        }
        var active = center.getActiveTab();
        saveState({
            index: index,
            active: !!(active && active.id === TAB_ID)
        });
    }

    // dropTab возвращает состояние панели без нашей вкладки.
    //
    // Панель кладёт в cookie карту «идентификатор вкладки → место» и при
    // загрузке пересобирает по ней разделы — но только если число вкладок в
    // панели совпадает с числом записей в карте. Наша вкладка в карту попадает
    // (она стоит в том же контейнере), а при загрузке её ещё нет: числа
    // расходятся, панель молча пропускает сборку и оставляет разделы в порядке
    // по умолчанию. Поэтому в cookie уходит карта без нас — иначе наша вкладка
    // ломает перестановку всех остальных разделов.
    function dropTab(center, state) {
        var tab = Ext.getCmp(TAB_ID);
        var at = tab ? center.items.indexOf(tab) : -1;
        if (at < 0 || !state) {
            return null; // нашей вкладки в панели нет, чистить нечего
        }

        var clean = Ext.apply({}, state);
        if (state.tabsMap) {
            var map = {};
            var next = 0;
            for (var id in state.tabsMap) {
                if (state.tabsMap.hasOwnProperty(id) && id !== TAB_ID) {
                    map[id] = next++;
                }
            }
            clean.tabsMap = map; // копия: у панели тут живая карта контейнера
        }

        // Открытый раздел панель запоминает номером. Без нашей вкладки номера
        // правее неё сдвигаются на один; а если открыта она сама — отдаём
        // соседний раздел, свою вкладку мы откроем сами, по своей записи.
        if (typeof clean.activeTab === "number" && clean.activeTab >= at) {
            clean.activeTab = Math.max(clean.activeTab - 1, 0);
        }
        return clean;
    }

    function attach(center) {
        if (center.mqttWatched) {
            return;
        }
        center.mqttWatched = true;

        // Переключение вкладок меняет признак открытости — ловится здесь.
        center.on("tabchange", function () { remember(center); });
        center.on("add", function () { remember(center); });

        // Перетаскивание вкладок никаких событий контейнеру не приносит:
        // порядок меняется вызовом move, а tabchange к этому моменту уже
        // прошёл — со старым местом. Зато панель сохраняет состояние сразу
        // после перестановки, и вот тут место уже новое.
        center.on("statesave", function (panel, state) {
            var clean = dropTab(center, state);
            if (clean) {
                Ext.state.Manager.set(panel.getStateId(), clean);
            }
            remember(center);
        });
    }

    function ensureTab() {
        var center = Ext.getCmp("centerPanel");
        if (!center || !center.add) {
            return false; // ещё не вошли в панель
        }

        attach(center);
        if (Ext.getCmp(TAB_ID)) {
            remember(center);
        } else {
            place(center);
        }
        return true;
    }

    // Панель сохраняет состояние с задержкой в пару секунд, и обновление
    // страницы сразу после перетаскивания её опережает. Место снимается ещё и
    // здесь: запись в localStorage синхронная и уйти со страницы не мешает.
    function rememberOnLeave() {
        var center = Ext.getCmp("centerPanel");
        if (center && center.items) {
            remember(center);
        }
    }

    function start() {
        // Сначала ждём появления панели часто, потом присматриваем редко: она
        // пересобирает вкладки при восстановлении состояния и после повторного
        // входа, и наша вкладка иначе исчезает.
        //
        // Интервал перепланируется на каждом шаге, а не задаётся один раз:
        // с setInterval задержка вычислилась бы до входа в панель и осталась
        // частой навсегда.
        function tick() {
            var found = ensureTab();
            setTimeout(tick, found ? WATCH_EVERY : CHECK_EVERY);
        }
        tick();

        window.addEventListener("pagehide", rememberOnLeave);
        window.addEventListener("beforeunload", rememberOnLeave);
    }

    if (typeof Ext === "undefined") {
        window.addEventListener("load", start);
    } else {
        Ext.onReady(start);
    }
})();
