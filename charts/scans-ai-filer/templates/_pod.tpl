{{- define "scans-ai-filer.podSpec" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- if .Values.ocrSidecar.enabled }}
initContainers:
  - name: ocr-sidecar
    image: "{{ .Values.ocrSidecar.image.repository }}:{{ .Values.ocrSidecar.image.tag | default .Chart.AppVersion }}"
    imagePullPolicy: {{ .Values.ocrSidecar.image.pullPolicy }}
    restartPolicy: Always
    securityContext:
      {{- toYaml .Values.securityContext | nindent 6 }}
    env:
      - name: OCR_LISTEN_ADDR
        value: ":{{ .Values.ocrSidecar.port }}"
      - name: OCR_LANG
        value: {{ .Values.ocrSidecar.lang | quote }}
    ports:
      - containerPort: {{ .Values.ocrSidecar.port }}
    readinessProbe:
      httpGet:
        path: /healthz
        port: {{ .Values.ocrSidecar.port }}
      initialDelaySeconds: 1
      periodSeconds: 2
    resources:
      {{- toYaml .Values.ocrSidecar.resources | nindent 6 }}
    volumeMounts:
      - name: ocr-tmp
        mountPath: /tmp
{{- end }}
containers:
  - name: {{ .Chart.Name }}
    image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
    imagePullPolicy: {{ .Values.image.pullPolicy }}
    {{- if .Values.webUI.enabled }}
    ports:
      - name: http
        containerPort: {{ .Values.webUI.port }}
    readinessProbe:
      httpGet:
        path: /healthz
        port: http
      initialDelaySeconds: 2
      periodSeconds: 10
    {{- end }}
    securityContext:
      {{- toYaml .Values.securityContext | nindent 6 }}
    env:
      - name: STORAGE_PROVIDER
        value: {{ .Values.config.storageProvider | quote }}
      {{- if .Values.config.localRootPath }}
      - name: LOCAL_ROOT_PATH
        value: {{ .Values.config.localRootPath | quote }}
      {{- end }}
      - name: AI_PROVIDER
        value: {{ .Values.config.aiProvider | quote }}
      - name: GEMINI_MODEL
        value: {{ .Values.config.geminiModel | quote }}
      {{- if eq .Values.workloadMode "deployment" }}
      - name: POLL_INTERVAL_SECONDS
        value: {{ .Values.config.pollIntervalSeconds | default 60 | quote }}
      {{- end }}
      {{- if .Values.config.inboxRoutes }}
      - name: INBOX_ROUTES
        value: {{ .Values.config.inboxRoutes | quote }}
      {{- end }}
      {{- if .Values.config.ollamaBaseUrl }}
      - name: OLLAMA_BASE_URL
        value: {{ .Values.config.ollamaBaseUrl | quote }}
      {{- end }}
      {{- if .Values.config.ollamaModel }}
      - name: OLLAMA_MODEL
        value: {{ .Values.config.ollamaModel | quote }}
      {{- end }}
      {{- if .Values.config.ollamaTextModel }}
      - name: OLLAMA_TEXT_MODEL
        value: {{ .Values.config.ollamaTextModel | quote }}
      {{- end }}
      {{- if .Values.config.ollamaMaxPages }}
      - name: OLLAMA_MAX_PAGES
        value: {{ .Values.config.ollamaMaxPages | quote }}
      {{- end }}
      {{- if .Values.config.ollamaMaxImageDim }}
      - name: OLLAMA_MAX_IMAGE_DIM
        value: {{ .Values.config.ollamaMaxImageDim | quote }}
      {{- end }}
      {{- if .Values.config.ollamaTimeoutSeconds }}
      - name: OLLAMA_TIMEOUT_SECONDS
        value: {{ .Values.config.ollamaTimeoutSeconds | quote }}
      {{- end }}
      {{- if .Values.config.ocrProvider }}
      - name: OCR_PROVIDER
        value: {{ .Values.config.ocrProvider | quote }}
      {{- end }}
      {{- if .Values.config.ocrEndpointUrl }}
      - name: OCR_ENDPOINT_URL
        value: {{ .Values.config.ocrEndpointUrl | quote }}
      {{- else if .Values.ocrSidecar.enabled }}
      - name: OCR_ENDPOINT_URL
        value: "http://127.0.0.1:{{ .Values.ocrSidecar.port }}/ocr"
      {{- end }}
      {{- if .Values.config.ocrLang }}
      - name: OCR_LANG
        value: {{ .Values.config.ocrLang | quote }}
      {{- end }}
      {{- if .Values.config.ocrMinChars }}
      - name: OCR_MIN_CHARS
        value: {{ .Values.config.ocrMinChars | quote }}
      {{- end }}
      {{- if .Values.config.ocrVisualGlanceDim }}
      - name: OCR_VISUAL_GLANCE_DIM
        value: {{ .Values.config.ocrVisualGlanceDim | quote }}
      {{- end }}
      {{- if .Values.config.ocrHybridRemote }}
      - name: OCR_HYBRID_REMOTE
        value: {{ .Values.config.ocrHybridRemote | quote }}
      {{- end }}
      {{- if .Values.config.ocrEmbedPdf }}
      - name: OCR_EMBED_PDF
        value: {{ .Values.config.ocrEmbedPdf | quote }}
      {{- end }}
      {{- if .Values.config.taxonomyDbPath }}
      - name: TAXONOMY_DB_PATH
        value: {{ .Values.config.taxonomyDbPath | quote }}
      {{- else if .Values.persistence.enabled }}
      - name: TAXONOMY_DB_PATH
        value: "{{ .Values.persistence.mountPath }}/taxonomy.db"
      {{- end }}
      {{- if .Values.config.embeddingProvider }}
      - name: EMBEDDING_PROVIDER
        value: {{ .Values.config.embeddingProvider | quote }}
      {{- end }}
      {{- if .Values.config.embeddingModel }}
      - name: EMBEDDING_MODEL
        value: {{ .Values.config.embeddingModel | quote }}
      {{- end }}
      {{- if .Values.config.taxonomyTopK }}
      - name: TAXONOMY_TOP_K
        value: {{ .Values.config.taxonomyTopK | quote }}
      {{- end }}
      - name: SCANS_FOLDER_NAME
        value: {{ .Values.config.scansFolderName | quote }}
      - name: INBOX_FOLDER_NAME
        value: {{ .Values.config.inboxFolderName | quote }}
      - name: BATCH_SIZE
        value: {{ .Values.config.batchSize | quote }}
      - name: PAUSE_SECONDS
        value: {{ .Values.config.pauseSeconds | quote }}
      - name: DRY_RUN
        value: {{ .Values.config.dryRun | quote }}
      - name: NOTIFY_ON_SUCCESS
        value: {{ .Values.config.notifyOnSuccess | quote }}
      - name: NOTIFY_ON_ERROR
        value: {{ .Values.config.notifyOnError | quote }}
      {{- if .Values.config.webhookUrl }}
      - name: WEBHOOK_URL
        value: {{ .Values.config.webhookUrl | quote }}
      {{- end }}
      {{- if ne (toString .Values.config.webhookOnSuccess) "" }}
      - name: WEBHOOK_ON_SUCCESS
        value: {{ .Values.config.webhookOnSuccess | quote }}
      {{- end }}
      {{- if ne (toString .Values.config.webhookOnError) "" }}
      - name: WEBHOOK_ON_ERROR
        value: {{ .Values.config.webhookOnError | quote }}
      {{- end }}
      {{- if .Values.config.ntfyUrl }}
      - name: NTFY_URL
        value: {{ .Values.config.ntfyUrl | quote }}
      {{- end }}
      {{- if .Values.config.ntfyTopic }}
      - name: NTFY_TOPIC
        value: {{ .Values.config.ntfyTopic | quote }}
      {{- end }}
      {{- if .Values.config.ntfyErrorTopic }}
      - name: NTFY_ERROR_TOPIC
        value: {{ .Values.config.ntfyErrorTopic | quote }}
      {{- end }}
      {{- if .Values.config.ntfyPriority }}
      - name: NTFY_PRIORITY
        value: {{ .Values.config.ntfyPriority | quote }}
      {{- end }}
      {{- if .Values.config.ntfyErrorPriority }}
      - name: NTFY_ERROR_PRIORITY
        value: {{ .Values.config.ntfyErrorPriority | quote }}
      {{- end }}
      {{- if .Values.config.ntfyClickUrl }}
      - name: NTFY_CLICK_URL
        value: {{ .Values.config.ntfyClickUrl | quote }}
      {{- end }}
      {{- if ne (toString .Values.config.ntfyOnSuccess) "" }}
      - name: NTFY_ON_SUCCESS
        value: {{ .Values.config.ntfyOnSuccess | quote }}
      {{- end }}
      {{- if ne (toString .Values.config.ntfyOnError) "" }}
      - name: NTFY_ON_ERROR
        value: {{ .Values.config.ntfyOnError | quote }}
      {{- end }}
      {{- if .Values.webUI.enabled }}
      - name: WEB_UI_ENABLED
        value: "true"
      - name: WEB_UI_ADDR
        value: ":{{ .Values.webUI.port }}"
      {{- end }}
      - name: GOOGLE_APPLICATION_CREDENTIALS
        value: "/etc/gdrive/{{ .Values.secrets.gdriveSaJsonKey }}"
      {{- if or .Values.taxonomy.existingConfigMap .Values.taxonomy.inlineCsv }}
      - name: TAXONOMY_CSV_PATH
        value: "/etc/scans-ai-filer/{{ .Values.taxonomy.key }}"
      {{- end }}
      - name: GEMINI_API_KEY
        valueFrom:
          secretKeyRef:
            name: {{ include "scans-ai-filer.secretName" . }}
            key: {{ .Values.secrets.geminiApiKeyKey }}
            optional: true
      {{- if .Values.secrets.pdfPasswordKey }}
      - name: PDF_PASSWORD
        valueFrom:
          secretKeyRef:
            name: {{ include "scans-ai-filer.secretName" . }}
            key: {{ .Values.secrets.pdfPasswordKey }}
            optional: true
      {{- end }}
      {{- if .Values.secrets.ntfyTokenKey }}
      - name: NTFY_TOKEN
        valueFrom:
          secretKeyRef:
            name: {{ include "scans-ai-filer.secretName" . }}
            key: {{ .Values.secrets.ntfyTokenKey }}
            optional: true
      {{- end }}
    resources:
      {{- toYaml .Values.resources | nindent 6 }}
    volumeMounts:
      - name: gdrive-sa
        mountPath: /etc/gdrive
        readOnly: true
      {{- if or .Values.taxonomy.existingConfigMap .Values.taxonomy.inlineCsv }}
      - name: taxonomy
        mountPath: /etc/scans-ai-filer
        readOnly: true
      {{- end }}
      {{- if .Values.persistence.enabled }}
      - name: data
        mountPath: {{ .Values.persistence.mountPath }}
      {{- end }}
volumes:
  - name: gdrive-sa
    secret:
      secretName: {{ include "scans-ai-filer.secretName" . }}
      items:
        - key: {{ .Values.secrets.gdriveSaJsonKey }}
          path: {{ .Values.secrets.gdriveSaJsonKey }}
  {{- if .Values.taxonomy.existingConfigMap }}
  - name: taxonomy
    configMap:
      name: {{ .Values.taxonomy.existingConfigMap }}
  {{- else if .Values.taxonomy.inlineCsv }}
  - name: taxonomy
    configMap:
      name: {{ include "scans-ai-filer.fullname" . }}-taxonomy
  {{- end }}
  {{- if .Values.persistence.enabled }}
  - name: data
    persistentVolumeClaim:
      claimName: {{ .Values.persistence.existingClaim | default (printf "%s-data" (include "scans-ai-filer.fullname" .)) }}
  {{- end }}
  {{- if .Values.ocrSidecar.enabled }}
  - name: ocr-tmp
    emptyDir: {}
  {{- end }}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
