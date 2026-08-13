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
        var active = center.getActiveTab();
        saveState({
            index: center.items.indexOf(tab),
            active: !!(active && active.id === TAB_ID)
        });
    }

    function attach(center) {
        if (center.mqttWatched) {
            return;
        }
        center.mqttWatched = true;

        // Переключение вкладок и перетаскивание меняют и место, и признак
        // открытости — оба события ловятся здесь.
        center.on("tabchange", function () { remember(center); });
        center.on("add", function () { remember(center); });
    }

    function ensureTab() {
        var center = Ext.getCmp("centerPanel");
        if (!center || !center.add) {
            return false; // ещё не вошли в панель
        }

        attach(center);
        if (!Ext.getCmp(TAB_ID)) {
            place(center);
        }
        return true;
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
    }

    if (typeof Ext === "undefined") {
        window.addEventListener("load", start);
    } else {
        Ext.onReady(start);
    }
})();
