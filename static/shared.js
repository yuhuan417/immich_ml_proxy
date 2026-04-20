function renderShellHeader(activePage, heroConfig) {
    const shell = document.querySelector('.shell');
    if (!shell) return;

    const main = shell.querySelector('.content');

    const header = document.createElement('header');
    header.className = 'masthead';
    header.innerHTML = `
        <div class="topbar">
            <div class="brand-block">
                <span class="brand-kicker">Immich ML Proxy</span>
                <span class="brand-title">${heroConfig.brandTitle || 'Operations Console'}</span>
            </div>
            <nav class="nav-tabs" aria-label="Primary">
                <a class="nav-link${activePage === 'config' ? ' active' : ''}" href="/config"${activePage === 'config' ? ' aria-current="page"' : ''}>Configuration</a>
                <a class="nav-link${activePage === 'debug' ? ' active' : ''}" href="/debug"${activePage === 'debug' ? ' aria-current="page"' : ''}>Debug Console</a>
            </nav>
        </div>
        <div class="hero">
            <div class="hero-copy">
                <h1>${heroConfig.title}</h1>
            </div>
            <div class="hero-status" id="heroStatus">
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

    function showNotice(message, type, opts) {
        const notice = document.getElementById('notice');
        if (!notice) return;

        type = type || 'info';
        opts = opts || {};

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

        notice.className = 'notice';
        notice.textContent = '';
    }

    return { showNotice: showNotice, hideNotice: hideNotice };
}
