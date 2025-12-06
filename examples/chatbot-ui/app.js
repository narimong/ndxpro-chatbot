class ChatApp {
    constructor() {
        this.serverUrl = '';
        this.sessionId = null;
        this.socket = null;
        this.isStreaming = false;
        this.currentBotMessage = null;

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
            // Send join event with session_id after connection
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

    sendMessage() {
        const content = this.messageInput.value.trim();
        if (!content || !this.socket || !this.socket.connected || this.isStreaming) {
            return;
        }

        this.addUserMessage(content);
        this.messageInput.value = '';
        this.messageInput.style.height = 'auto';

        this.socket.emit('chat', { content });

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
