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
 * Подключается строкой в index.php панели, после app.js:
 *   <script src="mqtt-tab.js"></script>
 */
(function () {
    "use strict";

    // Адрес шлюза. Тот же сервер, подкаталог за прокси — поэтому путь
    // относительный: панель и шлюз всегда на одном хосте.
    var GATEWAY_URL = "/mqtt/";
    var TAB_ID = "mqtt2mimismart";

    // Панель строится не сразу: centerPanel появляется только после входа,
    // а до этого на экране форма авторизации. Поэтому ждём, а не пытаемся
    // добавить вкладку один раз при загрузке.
    var CHECK_EVERY = 500;
    var GIVE_UP_AFTER = 120000; // две минуты: дольше человек просто не входил

    function addTab() {
        var center = Ext.getCmp("centerPanel");
        if (!center || !center.add) {
            return false;
        }
        if (Ext.getCmp(TAB_ID)) {
            return true; // уже добавлена
        }

        center.add({
            id: TAB_ID,
            title: "MQTT",
            xtype: "panel",
            layout: "fit",
            closable: false,
            // Шлюз показывается целиком, своим интерфейсом: повторять его
            // страницы на ExtJS значило бы вести две версии одного и того же.
            html:
                '<iframe src="' + GATEWAY_URL + '" ' +
                'style="border:0;width:100%;height:100%;display:block" ' +
                'title="Шлюз MQTT"></iframe>'
        });
        return true;
    }

    function waitForPanel() {
        var waited = 0;
        var timer = setInterval(function () {
            waited += CHECK_EVERY;
            if (addTab() || waited >= GIVE_UP_AFTER) {
                clearInterval(timer);
            }
        }, CHECK_EVERY);
    }

    // Ext может ещё не загрузиться, если файл подключён раньше времени.
    if (typeof Ext === "undefined") {
        window.addEventListener("load", waitForPanel);
    } else {
        Ext.onReady(waitForPanel);
    }
})();
