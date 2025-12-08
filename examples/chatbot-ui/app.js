class ChatApp {
    constructor() {
        this.serverUrl = '';
        this.sessionId = null;
        this.socket = null;
        this.isStreaming = false;
        this.currentBotMessage = null;
        this.debugMode = false;
        this.vizMode = true;

        // Viz data storage
        this.vizNodes = [];
        this.vizEdges = [];
        this.vizQueries = [];
        this.lastVizPayload = null;

        this.initElements();
        this.initEventListeners();
    }

    initElements() {
        this.serverUrlInput = document.getElementById('serverUrl');
        this.connectBtn = document.getElementById('connectBtn');
        this.sessionIdSpan = document.getElementById('sessionId');
        this.newSessionBtn = document.getElementById('newSessionBtn');
        this.clearHistoryBtn = document.getElementById('clearHistoryBtn');
        this.systemPromptInput = document.getElementById('systemPrompt');
        this.messagesContainer = document.getElementById('messages');
        this.messageInput = document.getElementById('messageInput');
        this.sendBtn = document.getElementById('sendBtn');
        this.connectionStatus = document.getElementById('connectionStatus');
        this.toastContainer = document.getElementById('toastContainer');

        // Debug mode elements
        this.debugModeToggle = document.getElementById('debugMode');
        this.debugModeLabel = document.getElementById('debugModeLabel');
        this.debugPanel = document.getElementById('debugPanel');
        this.debugContent = document.getElementById('debugContent');
        this.clearDebugBtn = document.getElementById('clearDebugBtn');

        // Viz mode elements
        this.vizModeToggle = document.getElementById('vizMode');
        this.vizModeLabel = document.getElementById('vizModeLabel');
        this.vizPanel = document.getElementById('vizPanel');
        this.vizEvents = document.getElementById('vizEvents');
        this.vizNodeCount = document.getElementById('vizNodeCount');
        this.vizEdgeCount = document.getElementById('vizEdgeCount');
        this.vizQueryCount = document.getElementById('vizQueryCount');
        this.clearVizBtn = document.getElementById('clearVizBtn');
    }

    initEventListeners() {
        this.connectBtn.addEventListener('click', () => this.handleConnect());
        this.newSessionBtn.addEventListener('click', () => this.createNewSession());
        this.clearHistoryBtn.addEventListener('click', () => this.clearHistory());
        this.sendBtn.addEventListener('click', () => this.sendMessage());

        this.messageInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                this.sendMessage();
            }
        });

        this.messageInput.addEventListener('input', () => {
            this.messageInput.style.height = 'auto';
            this.messageInput.style.height = Math.min(this.messageInput.scrollHeight, 120) + 'px';
        });

        // Debug mode toggle
        this.debugModeToggle.addEventListener('change', () => {
            this.debugMode = this.debugModeToggle.checked;
            this.debugModeLabel.textContent = this.debugMode ? 'ON' : 'OFF';
            this.debugModeLabel.classList.toggle('active', this.debugMode);
            this.debugPanel.style.display = this.debugMode ? 'flex' : 'none';
        });

        // Clear debug log
        this.clearDebugBtn.addEventListener('click', () => {
            this.clearDebugLog();
        });

        // Viz mode toggle
        this.vizModeToggle.addEventListener('change', () => {
            this.vizMode = this.vizModeToggle.checked;
            this.vizModeLabel.textContent = this.vizMode ? 'ON' : 'OFF';
            this.vizModeLabel.classList.toggle('active', this.vizMode);
            this.vizPanel.style.display = this.vizMode ? 'flex' : 'none';
        });

        // Clear viz log
        this.clearVizBtn.addEventListener('click', () => {
            this.clearVizLog();
        });

        // Initialize viz panel visibility
        this.vizPanel.style.display = this.vizMode ? 'flex' : 'none';
        this.vizModeLabel.classList.toggle('active', this.vizMode);
    }

    async handleConnect() {
        this.serverUrl = this.serverUrlInput.value.trim();
        if (!this.serverUrl) {
            this.showToast('서버 URL을 입력하세요', 'error');
            return;
        }

        this.connectBtn.disabled = true;
        this.connectBtn.textContent = '연결 중...';

        try {
            await this.createSession();
            this.showToast('연결되었습니다', 'success');
        } catch (error) {
            this.showToast(`연결 실패: ${error.message}`, 'error');
            this.connectBtn.disabled = false;
            this.connectBtn.textContent = '연결';
        }
    }

    async createSession() {
        const systemPrompt = this.systemPromptInput.value.trim();
        const body = systemPrompt ? JSON.stringify({ system_prompt: systemPrompt }) : '{}';

        const response = await fetch(`http://${this.serverUrl}/api/v1/sessions`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: body
        });

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const data = await response.json();
        this.sessionId = data.session_id;
        this.sessionIdSpan.textContent = this.sessionId.substring(0, 8) + '...';
        this.sessionIdSpan.title = this.sessionId;

        this.connect();
    }

    async createNewSession() {
        if (this.socket) {
            this.socket.disconnect();
        }

        this.clearMessages();
        this.clearDebugLog();
        this.clearVizLog();

        try {
            await this.createSession();
            this.showToast('새 세션이 생성되었습니다', 'success');
        } catch (error) {
            this.showToast(`세션 생성 실패: ${error.message}`, 'error');
        }
    }

    connect() {
        if (this.socket) {
            this.socket.disconnect();
        }

        this.socket = io(`http://${this.serverUrl}`, {
            transports: ['websocket'],
            reconnection: true,
            reconnectionAttempts: 5,
            reconnectionDelay: 1000
        });

        this.socket.on('connect', () => {
            console.log('Socket.IO connected, joining session:', this.sessionId);
            this.socket.emit('join', { session_id: this.sessionId });
        });

        this.socket.on('joined', (data) => {
            console.log('Joined session:', data.session_id);
            this.updateConnectionStatus(true);
            this.enableInput(true);
            this.connectBtn.textContent = '연결됨';
            this.clearWelcomeMessage();
        });

        this.socket.on('disconnect', () => {
            this.updateConnectionStatus(false);
            this.enableInput(false);
            this.connectBtn.disabled = false;
            this.connectBtn.textContent = '연결';
        });

        this.socket.on('connect_error', (error) => {
            console.error('Socket.IO connection error:', error);
            this.showToast(`연결 오류: ${error.message}`, 'error');
        });

        this.socket.on('chunk', (data) => {
            this.handleChunk(data);
        });

        this.socket.on('done', (data) => {
            this.handleDone(data);
        });

        this.socket.on('error', (data) => {
            this.handleError(data);
        });

        this.socket.on('cleared', () => {
            this.clearMessages();
            this.showToast('대화 내역이 초기화되었습니다', 'success');
        });

        // Debug event listeners
        this.socket.on('debug:step', (data) => {
            this.handleDebugStep(data);
        });

        this.socket.on('debug:plan', (data) => {
            this.handleDebugPlan(data);
        });

        this.socket.on('debug:query', (data) => {
            this.handleDebugQuery(data);
        });

        this.socket.on('debug:retrieval', (data) => {
            this.handleDebugRetrieval(data);
        });

        // Clarification event listener
        this.socket.on('clarification:request', (data) => {
            this.handleClarificationRequest(data);
        });

        // Viz event listeners (NEW)
        this.socket.on('viz:node', (data) => {
            console.log('[VIZ] Received viz:node:', data);
            this.handleVizNode(data);
        });

        this.socket.on('viz:edge', (data) => {
            console.log('[VIZ] Received viz:edge:', data);
            this.handleVizEdge(data);
        });

        this.socket.on('viz:cypher', (data) => {
            console.log('[VIZ] Received viz:cypher:', data);
            this.handleVizCypher(data);
        });

        this.socket.on('viz:complete', (data) => {
            console.log('[VIZ] Received viz:complete:', data);
            this.handleVizComplete(data);
        });
    }

    // ============================================
    // Viz Event Handlers (NEW)
    // ============================================

    handleVizNode(data) {
        this.vizNodes.push(data);
        this.updateVizStats();

        if (this.vizMode) {
            const labels = data.labels ? data.labels.join(', ') : 'N/A';
            const score = data.score ? ` (score: ${data.score.toFixed(2)})` : '';
            const source = data.source ? ` [${data.source}]` : '';

            let detailsHtml = '';
            if (data.uuid) {
                detailsHtml += `<div class="detail-item">UUID: ${data.uuid.substring(0, 12)}...</div>`;
            }
            if (data.depth !== undefined) {
                detailsHtml += `<div class="detail-item">Depth: ${data.depth}</div>`;
            }

            this.addVizEvent('node', `${data.name} [${labels}]${score}${source}`, detailsHtml);
        }
    }

    handleVizEdge(data) {
        this.vizEdges.push(data);
        this.updateVizStats();

        if (this.vizMode) {
            const direction = data.direction || 'outgoing';
            const fromShort = data.from_uuid ? data.from_uuid.substring(0, 8) : '?';
            const toShort = data.to_uuid ? data.to_uuid.substring(0, 8) : '?';

            let detailsHtml = `<div class="detail-item">${fromShort}... → ${toShort}...</div>`;

            this.addVizEvent('edge', `${data.relationship} (${direction})`, detailsHtml);
        }
    }

    handleVizCypher(data) {
        this.vizQueries.push(data);
        this.updateVizStats();

        if (this.vizMode) {
            const cypherPreview = data.cypher && data.cypher.length > 50
                ? data.cypher.substring(0, 50) + '...'
                : (data.cypher || 'N/A');
            const duration = data.duration_ms ? `${data.duration_ms}ms` : 'N/A';
            const resultCount = data.result_count !== undefined ? data.result_count : '?';

            let detailsHtml = '';
            if (data.source) {
                detailsHtml += `<div class="detail-item">Source: ${data.source}</div>`;
            }

            this.addVizEvent('cypher', `${cypherPreview} (${resultCount} results, ${duration})`, detailsHtml);
        }
    }

    handleVizComplete(data) {
        this.lastVizPayload = data;

        if (this.vizMode) {
            const nodeCount = data.nodes ? data.nodes.length : 0;
            const edgeCount = data.edges ? data.edges.length : 0;
            const queryCount = data.cypher_queries ? data.cypher_queries.length : 0;

            let detailsHtml = '';
            if (data.hierarchy) {
                detailsHtml += `<div class="detail-item">Hierarchy root: ${data.hierarchy.root_uuid?.substring(0, 8)}...</div>`;
                if (data.hierarchy.tree) {
                    detailsHtml += `<div class="detail-item">Tree size: ${data.hierarchy.tree.length}</div>`;
                }
            }
            if (data.answer_node_ids && data.answer_node_ids.length > 0) {
                detailsHtml += `<div class="detail-item">Answer nodes: ${data.answer_node_ids.length}</div>`;
            }

            this.addVizEvent('complete', `${nodeCount} nodes, ${edgeCount} edges, ${queryCount} queries`, detailsHtml);
        }

        console.log('Viz Complete Payload:', data);
    }

    addVizEvent(type, content, detailsHtml = '') {
        // Remove placeholder if exists
        const placeholder = this.vizEvents.querySelector('.viz-placeholder');
        if (placeholder) {
            placeholder.remove();
        }

        const timestamp = new Date().toLocaleTimeString('ko-KR', {
            hour: '2-digit',
            minute: '2-digit',
            second: '2-digit',
            hour12: false
        });

        const entry = document.createElement('div');
        entry.className = `viz-event ${type}`;

        const typeLabel = {
            'node': 'NODE',
            'edge': 'EDGE',
            'cypher': 'CYPHER',
            'complete': 'COMPLETE'
        }[type] || type.toUpperCase();

        const emoji = {
            'node': '🔵',
            'edge': '🔗',
            'cypher': '📜',
            'complete': '✅'
        }[type] || '📣';

        entry.innerHTML = `
            <span class="viz-timestamp">${timestamp}</span>
            <span class="viz-type ${type}">${emoji} ${typeLabel}</span>
            <span class="viz-content">${content}</span>
            ${detailsHtml ? `<div class="viz-details">${detailsHtml}</div>` : ''}
        `;

        this.vizEvents.appendChild(entry);
        this.vizEvents.scrollTop = this.vizEvents.scrollHeight;
    }

    updateVizStats() {
        this.vizNodeCount.textContent = this.vizNodes.length;
        this.vizEdgeCount.textContent = this.vizEdges.length;
        this.vizQueryCount.textContent = this.vizQueries.length;
    }

    clearVizLog() {
        this.vizNodes = [];
        this.vizEdges = [];
        this.vizQueries = [];
        this.lastVizPayload = null;
        this.updateVizStats();

        this.vizEvents.innerHTML = `
            <div class="viz-placeholder">
                No visualization events yet. Send a message to start.
            </div>
        `;
    }

    // ============================================
    // Existing handlers
    // ============================================

    // Handle clarification request from server
    handleClarificationRequest(data) {
        console.log('Received clarification request:', data);

        // Add debug entry
        if (this.debugMode) {
            this.addDebugEntry('clarification', `명확화 필요: ${data.reason}`,
                `<div class="clarification-options">${data.options?.map((opt, i) =>
                    `<div class="clarification-option">${i+1}. ${opt.label}</div>`
                ).join('') || ''}</div>`, 'started');
        }

        // Create clarification UI
        this.showClarificationDialog(data);
    }

    // Show clarification dialog to user
    showClarificationDialog(data) {
        // Remove existing clarification dialog if any
        const existing = document.querySelector('.clarification-dialog');
        if (existing) existing.remove();

        const dialog = document.createElement('div');
        dialog.className = 'clarification-dialog';

        let optionsHtml = data.options?.map((opt, i) => `
            <button class="clarification-option-btn" data-id="${opt.id}" data-target="${opt.target_label}">
                <span class="option-label">${opt.label}</span>
                ${opt.description ? `<span class="option-desc">${opt.description}</span>` : ''}
            </button>
        `).join('') || '';

        dialog.innerHTML = `
            <div class="clarification-content">
                <div class="clarification-header">
                    <span class="clarification-icon">❓</span>
                    <span class="clarification-title">확인이 필요합니다</span>
                </div>
                <div class="clarification-reason">${data.reason || '어떤 정보를 원하시나요?'}</div>
                <div class="clarification-options">
                    ${optionsHtml}
                </div>
                ${data.allow_free_text ? `
                    <div class="clarification-freetext">
                        <input type="text" id="clarificationFreeText" placeholder="또는 직접 입력하세요..." />
                        <button id="clarificationFreeTextBtn">전송</button>
                    </div>
                ` : ''}
            </div>
        `;

        this.messagesContainer.appendChild(dialog);
        this.scrollToBottom();

        // Store request ID for response
        this.pendingClarificationRequestId = data.request_id;

        // Add click handlers for options
        dialog.querySelectorAll('.clarification-option-btn').forEach(btn => {
            btn.addEventListener('click', () => {
                const selectedId = btn.dataset.id;
                this.sendClarificationResponse(selectedId, null);
                dialog.remove();
            });
        });

        // Add handler for free text
        if (data.allow_free_text) {
            const freeTextInput = dialog.querySelector('#clarificationFreeText');
            const freeTextBtn = dialog.querySelector('#clarificationFreeTextBtn');

            freeTextBtn.addEventListener('click', () => {
                const text = freeTextInput.value.trim();
                if (text) {
                    this.sendClarificationResponse(null, text);
                    dialog.remove();
                }
            });

            freeTextInput.addEventListener('keydown', (e) => {
                if (e.key === 'Enter') {
                    const text = freeTextInput.value.trim();
                    if (text) {
                        this.sendClarificationResponse(null, text);
                        dialog.remove();
                    }
                }
            });
        }
    }

    // Send clarification response to server
    sendClarificationResponse(selectedId, freeText) {
        if (!this.socket || !this.socket.connected) {
            this.showToast('서버에 연결되어 있지 않습니다', 'error');
            return;
        }

        const response = {
            request_id: this.pendingClarificationRequestId,
            debug: this.debugMode
        };

        if (selectedId) {
            response.selected_id = selectedId;
        }
        if (freeText) {
            response.free_text = freeText;
        }

        console.log('Sending clarification response:', response);
        this.socket.emit('clarification:response', response);

        // Show loading state
        this.enableInput(false);

        // Add debug entry
        if (this.debugMode) {
            this.addDebugEntry('clarification',
                `사용자 선택: ${selectedId || freeText}`, '', 'completed');
        }
    }

    handleChunk(payload) {
        if (!this.isStreaming) {
            this.isStreaming = true;
            this.currentBotMessage = this.addBotMessage();
        }

        this.appendToCurrentMessage(payload.content);
    }

    handleDone(payload) {
        this.isStreaming = false;
        if (this.currentBotMessage) {
            this.currentBotMessage.classList.remove('streaming');
        }
        this.currentBotMessage = null;
        this.enableInput(true);
    }

    handleError(payload) {
        this.isStreaming = false;
        if (this.currentBotMessage) {
            this.currentBotMessage.classList.remove('streaming');
            this.currentBotMessage.classList.add('error');
        }
        this.currentBotMessage = null;
        this.enableInput(true);
        this.showToast(`오류: ${payload.message}`, 'error');
    }

    // Debug event handlers
    handleDebugStep(data) {
        const statusClass = data.status || '';
        const statusText = data.status === 'started' ? '시작' :
                          data.status === 'completed' ? '완료' :
                          data.status === 'error' ? '오류' : data.status;

        let detailsHtml = '';
        if (data.duration_ms) {
            detailsHtml += `<div class="detail-item"><span class="detail-key">소요시간:</span> <span class="detail-value">${data.duration_ms}ms</span></div>`;
        }
        if (data.input && Object.keys(data.input).length > 0) {
            detailsHtml += `<div class="detail-item"><span class="detail-key">입력:</span> <span class="detail-value">${JSON.stringify(data.input).substring(0, 100)}</span></div>`;
        }
        if (data.output && Object.keys(data.output).length > 0) {
            detailsHtml += `<div class="detail-item"><span class="detail-key">출력:</span> <span class="detail-value">${JSON.stringify(data.output).substring(0, 100)}</span></div>`;
        }

        this.addDebugEntry('step', `${data.step_name} - ${statusText}`, detailsHtml, statusClass);
    }

    handleDebugPlan(data) {
        let content = `상태: ${data.status}`;
        if (data.objective) {
            content = `목표: ${data.objective}`;
        }

        let detailsHtml = '';
        if (data.steps && data.steps.length > 0) {
            detailsHtml = '<div class="plan-steps">';
            data.steps.forEach((step, index) => {
                const indexClass = index === data.current_step ? 'current' :
                                  (data.status === 'completed' ? 'completed' : '');
                detailsHtml += `
                    <div class="plan-step">
                        <span class="plan-step-index ${indexClass}">${index + 1}</span>
                        <span>${step}</span>
                    </div>`;
            });
            detailsHtml += '</div>';
        }

        this.addDebugEntry('plan', content, detailsHtml);
    }

    handleDebugQuery(data) {
        const content = `원본 쿼리: "${data.original_query}"`;

        let detailsHtml = '<div class="query-list">';
        detailsHtml += `<div class="query-item original">→ ${data.original_query}</div>`;

        if (data.rewritten_queries && data.rewritten_queries.length > 0) {
            data.rewritten_queries.forEach((query, index) => {
                if (query !== data.original_query) {
                    detailsHtml += `<div class="query-item rewritten">→ ${query}</div>`;
                }
            });
        }
        detailsHtml += '</div>';

        this.addDebugEntry('query', `쿼리 리라이팅 (${data.rewritten_queries?.length || 1}개)`, detailsHtml);
    }

    handleDebugRetrieval(data) {
        const content = `쿼리 #${data.query_index + 1}: ${data.document_count}개 문서 검색 (${data.duration_ms}ms)`;

        let detailsHtml = '';
        if (data.documents && data.documents.length > 0) {
            detailsHtml = '<div class="debug-details">';
            data.documents.forEach((doc, index) => {
                const score = doc.score ? ` (${(doc.score * 100).toFixed(1)}%)` : '';
                detailsHtml += `<div class="detail-item"><span class="detail-key">[${doc.id}]${score}</span> ${doc.content.substring(0, 80)}...</div>`;
            });
            detailsHtml += '</div>';
        }

        this.addDebugEntry('retrieval', content, detailsHtml);
    }

    addDebugEntry(type, content, detailsHtml = '', statusClass = '') {
        const timestamp = new Date().toLocaleTimeString('ko-KR', {
            hour: '2-digit',
            minute: '2-digit',
            second: '2-digit',
            hour12: false
        });

        const entry = document.createElement('div');
        entry.className = `debug-entry ${type} ${statusClass}`;

        const typeLabel = {
            'step': 'STEP',
            'plan': 'PLAN',
            'query': 'QUERY',
            'retrieval': 'RETRIEVAL'
        }[type] || type.toUpperCase();

        entry.innerHTML = `
            <span class="debug-timestamp">${timestamp}</span>
            <span class="debug-type ${type}">[${typeLabel}]</span>
            <span class="debug-content-text">${content}</span>
            ${detailsHtml}
        `;

        this.debugContent.appendChild(entry);
        this.debugContent.scrollTop = this.debugContent.scrollHeight;
    }

    clearDebugLog() {
        this.debugContent.innerHTML = '';
    }

    sendMessage() {
        const content = this.messageInput.value.trim();
        if (!content || !this.socket || !this.socket.connected || this.isStreaming) {
            return;
        }

        this.addUserMessage(content);
        this.messageInput.value = '';
        this.messageInput.style.height = 'auto';

        // Clear logs for new message
        if (this.debugMode) {
            this.clearDebugLog();
        }
        if (this.vizMode) {
            this.clearVizLog();
        }

        // Send with debug flag (viz mode also requires debug mode on server)
        const debugFlag = this.debugMode || this.vizMode;
        console.log('[VIZ] Sending chat with debug:', debugFlag, 'debugMode:', this.debugMode, 'vizMode:', this.vizMode);
        this.socket.emit('chat', {
            content,
            debug: debugFlag
        });

        this.enableInput(false);
    }

    clearHistory() {
        if (!this.socket || !this.socket.connected) {
            this.showToast('서버에 연결되어 있지 않습니다', 'error');
            return;
        }

        this.socket.emit('clear');
    }

    addUserMessage(content) {
        const message = this.createMessageElement('user', content);
        this.messagesContainer.appendChild(message);
        this.scrollToBottom();
    }

    addBotMessage() {
        const message = this.createMessageElement('bot', '');
        message.classList.add('streaming');
        this.messagesContainer.appendChild(message);
        this.scrollToBottom();
        return message;
    }

    createMessageElement(type, content) {
        const message = document.createElement('div');
        message.className = `message ${type}`;

        const label = document.createElement('div');
        label.className = 'message-label';
        label.textContent = type === 'user' ? 'You' : 'Bot';

        const contentDiv = document.createElement('div');
        contentDiv.className = 'message-content';
        contentDiv.textContent = content;

        message.appendChild(label);
        message.appendChild(contentDiv);

        return message;
    }

    appendToCurrentMessage(content) {
        if (this.currentBotMessage) {
            const contentDiv = this.currentBotMessage.querySelector('.message-content');
            contentDiv.textContent += content;
            this.scrollToBottom();
        }
    }

    clearMessages() {
        this.messagesContainer.innerHTML = '';
    }

    clearWelcomeMessage() {
        const welcome = this.messagesContainer.querySelector('.welcome-message');
        if (welcome) {
            welcome.remove();
        }
    }

    scrollToBottom() {
        this.messagesContainer.scrollTop = this.messagesContainer.scrollHeight;
    }

    updateConnectionStatus(connected) {
        if (connected) {
            this.connectionStatus.classList.add('connected');
            this.connectionStatus.querySelector('.status-text').textContent = '연결됨';
        } else {
            this.connectionStatus.classList.remove('connected');
            this.connectionStatus.querySelector('.status-text').textContent = '연결 안됨';
        }
    }

    enableInput(enabled) {
        this.messageInput.disabled = !enabled;
        this.sendBtn.disabled = !enabled;
        if (enabled) {
            this.messageInput.focus();
        }
    }

    showToast(message, type = 'info') {
        const toast = document.createElement('div');
        toast.className = `toast ${type}`;
        toast.textContent = message;

        this.toastContainer.appendChild(toast);

        setTimeout(() => {
            toast.remove();
        }, 3000);
    }
}

document.addEventListener('DOMContentLoaded', () => {
    window.chatApp = new ChatApp();
});
