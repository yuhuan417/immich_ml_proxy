function renderShellHeader(activePage, heroConfig) {
    const shell = document.querySelector('.shell');
    if (!shell) return;

    const main = shell.querySelector('.content');
    const brandTitle = heroConfig.brandTitle || 'Immich ML Proxy';

    const header = document.createElement('header');
    header.className = 'appbar';
    header.innerHTML = `
        <div class="appbar-row">
            <h1 class="visually-hidden">${heroConfig.title || brandTitle}</h1>
            <a class="brand" href="/config">${brandTitle}</a>
            <nav class="nav-tabs" aria-label="Primary">
                <a class="nav-link${activePage === 'config' ? ' active' : ''}" href="/config"${activePage === 'config' ? ' aria-current="page"' : ''}>Config</a>
                <a class="nav-link${activePage === 'debug' ? ' active' : ''}" href="/debug"${activePage === 'debug' ? ' aria-current="page"' : ''}>Debug</a>
            </nav>
            <div class="appbar-status" id="heroStatus">
                ${heroConfig.statusHTML || ''}
            </div>
        </div>
    `;

    if (main) {
        shell.insertBefore(header, main);
    } else {
        shell.appendChild(header);
    }
}

function createNoticeManager() {
    let noticeTimer = null;
    let dismissBound = false;

    function showNotice(message, type, opts) {
        const notice = document.getElementById('notice');
        if (!notice) return;

        type = type || 'info';
        opts = opts || {};

        if (!dismissBound) {
            // Tapping the toast clears it early; useful on touch devices.
            notice.addEventListener('click', hideNotice);
            dismissBound = true;
        }

        if (noticeTimer) {
            clearTimeout(noticeTimer);
            noticeTimer = null;
        }

        notice.textContent = message;
        notice.className = 'notice show ' + type;

        if (!opts.persist) {
            noticeTimer = setTimeout(function() { hideNotice(); }, 3200);
        }
    }

    function hideNotice() {
        const notice = document.getElementById('notice');
        if (!notice) return;

        if (noticeTimer) {
            clearTimeout(noticeTimer);
            noticeTimer = null;
        }

        notice.className = 'notice';
        notice.textContent = '';
    }

    return { showNotice: showNotice, hideNotice: hideNotice };
}
