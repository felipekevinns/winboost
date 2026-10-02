package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

type Tweak struct {
	ID     string
	Cat    string
	Title  string
	Desc   string
	Risk   string // safe, moderate, advanced
	Rec    bool   // recomendado
	Reboot bool
	Kind   string // toggle, action
	Group  string // grupo exclusivo (só um ativo)
	Warn   string
	Dur    string
	Apply  []Action
	Revert []Action
	Detect func() int // 1 aplicado, 0 não, -1 indisponível
	// CatalogRevert força o uso das ações Revert (ignora o backup de valores)
	CatalogRevert bool
}

type Category struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Icon  string `json:"icon"`
	Kind  string `json:"kind"`
}

var categories = []Category{
	{"perf", "Desempenho", "Energia, efeitos visuais e prioridade de processos", "bolt", "toggle"},
	{"svc", "Serviços", "Serviços do Windows que rodam sem você precisar", "layers", "toggle"},
	{"priv", "Privacidade", "Telemetria, anúncios e sugestões", "shield", "toggle"},
	{"ui", "Interface", "Barra de tarefas, Explorer e menu Iniciar", "layout", "toggle"},
	{"clean", "Limpeza", "Libere espaço em disco", "trash", "action"},
	{"maint", "Manutenção", "Reparos e verificações do sistema", "wrench", "action"},
}

const (
	hklm = "HKLM"
	hkcu = "HKCU"

	kPower      = `SYSTEM\CurrentControlSet\Control\Power`
	kVisual     = `Software\Microsoft\Windows\CurrentVersion\Explorer\VisualEffects`
	kDesktop    = `Control Panel\Desktop`
	kWinMetrics = `Control Panel\Desktop\WindowMetrics`
	kAdvanced   = `Software\Microsoft\Windows\CurrentVersion\Explorer\Advanced`
	kExplorer   = `Software\Microsoft\Windows\CurrentVersion\Explorer`
	kDWM        = `Software\Microsoft\Windows\DWM`
	kPersonal   = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
	kCDM        = `Software\Microsoft\Windows\CurrentVersion\ContentDeliveryManager`
	kPolWin     = `SOFTWARE\Policies\Microsoft\Windows`

	guidHigh     = "8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c"
	guidUltimate = "e9a42b02-d5df-448d-aa00-03f14749eb61"
	guidBalanced = "381b4222-f694-41f0-9685-ff5bb260df2e"
)

func activePowerScheme() string {
	v := regRead(hklm, `SYSTEM\CurrentControlSet\Control\Power\User\PowerSchemes`, "ActivePowerScheme")
	return strings.ToLower(v.S)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// serviço simples: desativar <-> padrão
func svcTweak(id, name, title, desc, def, risk string, rec bool, warn string) *Tweak {
	return &Tweak{
		ID: id, Cat: "svc", Title: title, Desc: desc, Risk: risk, Rec: rec, Kind: "toggle", Warn: warn,
		Apply:  []Action{Svc(name, "disabled")},
		Revert: []Action{Svc(name, def)},
	}
}

func svcGroup(id, title, desc string, names []string, def, risk string, rec bool, warn string) *Tweak {
	t := &Tweak{ID: id, Cat: "svc", Title: title, Desc: desc, Risk: risk, Rec: rec, Kind: "toggle", Warn: warn}
	for _, n := range names {
		t.Apply = append(t.Apply, Svc(n, "disabled"))
		t.Revert = append(t.Revert, Svc(n, def))
	}
	return t
}

var tweaks []*Tweak
var tweakByID = map[string]*Tweak{}

func buildCatalog() {
	add := func(t ...*Tweak) {
		for _, x := range t {
			tweaks = append(tweaks, x)
			tweakByID[x.ID] = x
		}
	}

	// ===================================================================
	// DESEMPENHO
	// ===================================================================
	add(&Tweak{
		ID: "power_high", Cat: "perf", Kind: "toggle", Group: "power", Risk: "safe", Rec: true,
		Title: "Plano de energia Alto Desempenho",
		Desc:  "Mantém o processador sempre na frequência máxima, sem economizar energia. Ideal para PCs de mesa.",
		Apply: []Action{Cmd("Ativando Alto Desempenho",
			"powercfg -duplicatescheme "+guidHigh+" "+guidHigh+" >nul 2>&1 & powercfg -setactive "+guidHigh)},
		Revert: []Action{Cmd("Voltando ao plano Equilibrado", "powercfg -setactive "+guidBalanced)},
		Detect: func() int { return b2i(activePowerScheme() == guidHigh) },
	})
	add(&Tweak{
		ID: "power_ultimate", Cat: "perf", Kind: "toggle", Group: "power", Risk: "moderate",
		Title: "Plano Desempenho Máximo (Ultimate)",
		Desc:  "Plano oculto do Windows que elimina até as micro-economias de energia. Ganho pequeno sobre o Alto Desempenho e mais consumo.",
		Apply: []Action{Cmd("Ativando Desempenho Máximo",
			"powercfg -duplicatescheme "+guidUltimate+" "+guidUltimate+" >nul 2>&1 & powercfg -setactive "+guidUltimate)},
		Revert: []Action{Cmd("Voltando ao plano Equilibrado", "powercfg -setactive "+guidBalanced)},
		Detect: func() int { return b2i(activePowerScheme() == guidUltimate) },
	})
	add(&Tweak{
		ID: "power_throttling", Cat: "perf", Kind: "toggle", Risk: "moderate", Reboot: true,
		Title:  "Desativar Power Throttling",
		Desc:   "Impede o Windows de reduzir a velocidade de programas em segundo plano para economizar energia.",
		Apply:  []Action{Reg(hklm, kPower+`\PowerThrottling`, "PowerThrottlingOff", 1)},
		Revert: []Action{RegDel(hklm, kPower+`\PowerThrottling`, "PowerThrottlingOff")},
	})
	add(&Tweak{
		ID: "hibernate", Cat: "perf", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Desativar hibernação e inicialização rápida",
		Desc:   "Apaga o arquivo hiberfil.sys (≈40% da RAM em disco) e evita que o Windows “congele” problemas entre desligamentos.",
		Apply:  []Action{Cmd("Desativando hibernação", "powercfg -h off")},
		Revert: []Action{Cmd("Reativando hibernação", "powercfg -h on")},
		Detect: func() int {
			v := regRead(hklm, kPower, "HibernateEnabled")
			return b2i(v.Exists && v.DW == 0)
		},
	})
	add(&Tweak{
		ID: "visual_fx", Cat: "perf", Kind: "toggle", Risk: "safe", Rec: true, Reboot: true,
		Title: "Efeitos visuais: melhor desempenho",
		Desc:  "Desliga animações de janelas, sombras e fades. Mantém as fontes suaves (ClearType).",
		Apply: []Action{
			Reg(hkcu, kVisual, "VisualFXSetting", 2),
			RegB(hkcu, kDesktop, "UserPreferencesMask", []byte{0x90, 0x12, 0x03, 0x80, 0x10, 0, 0, 0}),
			RegS(hkcu, kDesktop, "FontSmoothing", "2"),
			RegS(hkcu, kWinMetrics, "MinAnimate", "0"),
			Reg(hkcu, kAdvanced, "TaskbarAnimations", 0),
			Reg(hkcu, kAdvanced, "ListviewShadow", 0),
			Reg(hkcu, kDWM, "EnableAeroPeek", 0),
		},
		Revert: []Action{
			Reg(hkcu, kVisual, "VisualFXSetting", 0),
			RegB(hkcu, kDesktop, "UserPreferencesMask", []byte{0x9e, 0x1e, 0x07, 0x80, 0x12, 0, 0, 0}),
			RegS(hkcu, kWinMetrics, "MinAnimate", "1"),
			Reg(hkcu, kAdvanced, "TaskbarAnimations", 1),
			Reg(hkcu, kAdvanced, "ListviewShadow", 1),
			Reg(hkcu, kDWM, "EnableAeroPeek", 1),
		},
		Detect: func() int {
			v := regRead(hkcu, kVisual, "VisualFXSetting")
			m := regRead(hkcu, kWinMetrics, "MinAnimate")
			return b2i(v.DW == 2 && m.S == "0")
		},
	})
	add(&Tweak{
		ID: "transparency", Cat: "perf", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Desativar transparência",
		Desc:   "Remove o efeito de vidro/acrílico do Iniciar e da barra de tarefas. Alivia a placa de vídeo integrada.",
		Apply:  []Action{Reg(hkcu, kPersonal, "EnableTransparency", 0)},
		Revert: []Action{Reg(hkcu, kPersonal, "EnableTransparency", 1)},
	})
	add(&Tweak{
		ID: "menu_delay", Cat: "perf", Kind: "toggle", Risk: "safe", Rec: true, Reboot: true,
		Title:  "Menus mais rápidos",
		Desc:   "Reduz o atraso para abrir submenus de 400 ms para 100 ms.",
		Apply:  []Action{RegS(hkcu, kDesktop, "MenuShowDelay", "100")},
		Revert: []Action{RegS(hkcu, kDesktop, "MenuShowDelay", "400")},
	})
	add(&Tweak{
		ID: "fg_priority", Cat: "perf", Kind: "toggle", Risk: "moderate", Reboot: true,
		Title:  "Prioridade para o programa em uso",
		Desc:   "Dá fatias maiores de CPU para a janela em primeiro plano (Win32PrioritySeparation = 0x26).",
		Apply:  []Action{Reg(hklm, `SYSTEM\CurrentControlSet\Control\PriorityControl`, "Win32PrioritySeparation", 0x26)},
		Revert: []Action{Reg(hklm, `SYSTEM\CurrentControlSet\Control\PriorityControl`, "Win32PrioritySeparation", 2)},
	})
	add(&Tweak{
		ID: "gamedvr", Cat: "perf", Kind: "toggle", Risk: "safe", Rec: true,
		Title: "Desativar Game DVR / gravação em segundo plano",
		Desc:  "Impede a Xbox Game Bar de gravar a tela em segundo plano.",
		Apply: []Action{
			Reg(hkcu, `System\GameConfigStore`, "GameDVR_Enabled", 0),
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\GameDVR`, "AppCaptureEnabled", 0),
			Reg(hklm, kPolWin+`\GameDVR`, "AllowGameDVR", 0),
		},
		Revert: []Action{
			Reg(hkcu, `System\GameConfigStore`, "GameDVR_Enabled", 1),
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\GameDVR`, "AppCaptureEnabled", 1),
			RegDel(hklm, kPolWin+`\GameDVR`, "AllowGameDVR"),
		},
	})
	add(&Tweak{
		ID: "bg_apps", Cat: "perf", Kind: "toggle", Risk: "moderate", Rec: true,
		Title:  "Bloquear apps da Store em segundo plano",
		Desc:   "Apps como Clima, Fotos e Notícias só rodam quando você os abre. Programas tradicionais não são afetados.",
		Warn:   "Apps da Microsoft Store podem deixar de mostrar notificações com eles fechados.",
		Apply:  []Action{Reg(hklm, kPolWin+`\AppPrivacy`, "LetAppsRunInBackground", 2)},
		Revert: []Action{RegDel(hklm, kPolWin+`\AppPrivacy`, "LetAppsRunInBackground")},
	})
	add(&Tweak{
		ID: "edge_bg", Cat: "perf", Kind: "toggle", Risk: "safe", Rec: true,
		Title: "Impedir o Edge de rodar escondido",
		Desc:  "Desliga o “Startup Boost” e o modo em segundo plano do Microsoft Edge.",
		Apply: []Action{
			Reg(hklm, `SOFTWARE\Policies\Microsoft\Edge`, "StartupBoostEnabled", 0),
			Reg(hklm, `SOFTWARE\Policies\Microsoft\Edge`, "BackgroundModeEnabled", 0),
		},
		Revert: []Action{
			RegDel(hklm, `SOFTWARE\Policies\Microsoft\Edge`, "StartupBoostEnabled"),
			RegDel(hklm, `SOFTWARE\Policies\Microsoft\Edge`, "BackgroundModeEnabled"),
		},
	})
	add(&Tweak{
		ID: "chrome_bg", Cat: "perf", Kind: "toggle", Risk: "safe",
		Title:  "Impedir o Chrome de rodar escondido",
		Desc:   "Fecha o Chrome por completo ao fechar a última janela.",
		Warn:   "O Chrome passará a exibir “Gerenciado pela sua organização” (é apenas esta política).",
		Apply:  []Action{Reg(hklm, `SOFTWARE\Policies\Google\Chrome`, "BackgroundModeEnabled", 0)},
		Revert: []Action{RegDel(hklm, `SOFTWARE\Policies\Google\Chrome`, "BackgroundModeEnabled")},
	})
	add(&Tweak{
		ID: "last_access", Cat: "perf", Kind: "toggle", Risk: "safe",
		Title:  "Não registrar “último acesso” de arquivos",
		Desc:   "Evita uma escrita no disco toda vez que um arquivo é lido.",
		Apply:  []Action{Cmd("fsutil", "fsutil behavior set disablelastaccess 1")},
		Revert: []Action{Cmd("fsutil", "fsutil behavior set disablelastaccess 2")},
		Detect: func() int {
			v := regRead(hklm, `SYSTEM\CurrentControlSet\Control\FileSystem`, "NtfsDisableLastAccessUpdate")
			return b2i(v.Exists && v.DW == 1)
		},
	})

	// ===================================================================
	// SERVIÇOS
	// ===================================================================
	add(
		svcTweak("svc_sysmain", "SysMain", "Desativar SysMain (Superfetch)",
			"Pré-carrega programas lendo o disco o tempo todo. Uma das causas mais comuns de 100% de disco.",
			"auto", "safe", true, ""),
		&Tweak{ID: "svc_telemetry", Cat: "svc", Kind: "toggle", Risk: "safe", Rec: true,
			Title:  "Desativar serviços de telemetria",
			Desc:   "DiagTrack e dmwappushservice: coletam e enviam dados de uso para a Microsoft.",
			Apply:  []Action{Svc("DiagTrack", "disabled"), Svc("dmwappushservice", "disabled")},
			Revert: []Action{Svc("DiagTrack", "auto"), Svc("dmwappushservice", "demand")},
		},
		svcGroup("svc_xbox", "Desativar serviços do Xbox",
			"Login, salvamento em nuvem e rede do Xbox. Só servem para jogos da Store.",
			[]string{"XblAuthManager", "XblGameSave", "XboxNetApiSvc", "XboxGipSvc"}, "demand", "safe", true,
			"Controles de Xbox deixam de funcionar."),
		svcTweak("svc_maps", "MapsBroker", "Desativar mapas offline",
			"Baixa e atualiza mapas para o app Mapas.", "delayed-auto", "safe", true, ""),
		svcTweak("svc_retail", "RetailDemo", "Desativar modo de demonstração de loja",
			"Usado apenas em PCs de vitrine.", "demand", "safe", true, ""),
		svcTweak("svc_fax", "Fax", "Desativar Fax", "Envio e recebimento de fax pelo PC.", "demand", "safe", true, ""),
		svcTweak("svc_wmpnet", "WMPNetworkSvc", "Desativar compartilhamento do Media Player",
			"Compartilha bibliotecas de mídia na rede.", "demand", "safe", true, ""),
		svcTweak("svc_trkwks", "TrkWks", "Desativar rastreamento de links distribuídos",
			"Mantém atalhos para arquivos em outros PCs da rede. Raramente útil.", "auto", "safe", true, ""),
		svcTweak("svc_wpc", "WpcMonSvc", "Desativar controle dos pais",
			"Restrições de conta infantil da Família Microsoft.", "demand", "safe", true, ""),
		svcTweak("svc_insider", "wisvc", "Desativar serviço do Windows Insider",
			"Só necessário para receber versões de teste do Windows.", "demand", "safe", true, ""),
		svcTweak("svc_payments", "SEMgrSvc", "Desativar pagamentos e NFC",
			"Pagamentos por aproximação. Não usado em PCs de mesa.", "demand", "safe", true, ""),
		svcTweak("svc_wer", "WerSvc", "Desativar relatório de erros",
			"Gera e envia relatórios quando programas travam. Pode pesar no disco após falhas.",
			"demand", "safe", false, ""),
		svcTweak("svc_geo", "lfsvc", "Desativar geolocalização",
			"Localização para apps (clima, mapas, fuso automático).", "demand", "safe", false,
			"Fuso horário automático e apps de clima perdem a localização."),
		svcTweak("svc_bio", "WbioSrvc", "Desativar biometria (Windows Hello)",
			"Leitor de digital e reconhecimento facial.", "demand", "moderate", false,
			"Login por digital ou rosto para de funcionar."),
		svcTweak("svc_phone", "PhoneSvc", "Desativar telefonia",
			"Gerencia estado de telefonia para apps de chamada.", "demand", "safe", false, ""),
		svcTweak("svc_pca", "PcaSvc", "Desativar assistente de compatibilidade",
			"Monitora programas antigos para sugerir modos de compatibilidade.", "demand", "safe", false, ""),
		svcTweak("svc_bt", "bthserv", "Desativar Bluetooth",
			"Suporte a dispositivos Bluetooth.", "demand", "moderate", false,
			"Fones, mouses e teclados Bluetooth param de funcionar."),
		svcTweak("svc_search", "WSearch", "Desativar indexação da pesquisa",
			"Indexa arquivos e e-mails para buscas rápidas. Pode causar uso alto de disco.",
			"delayed-auto", "advanced", false,
			"A busca do menu Iniciar e do Outlook fica bem mais lenta."),
		svcTweak("svc_spooler", "Spooler", "Desativar spooler de impressão",
			"Necessário para qualquer impressão.", "auto", "advanced", false,
			"Você NÃO conseguirá imprimir nem usar “Microsoft Print to PDF”."),
	)

	// ===================================================================
	// PRIVACIDADE
	// ===================================================================
	add(&Tweak{
		ID: "telemetry", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Telemetria no mínimo",
		Desc:   "Reduz os dados de diagnóstico enviados à Microsoft ao nível mínimo permitido.",
		Apply:  []Action{Reg(hklm, kPolWin+`\DataCollection`, "AllowTelemetry", 0)},
		Revert: []Action{RegDel(hklm, kPolWin+`\DataCollection`, "AllowTelemetry")},
	})
	add(&Tweak{
		ID: "ad_id", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Desativar ID de publicidade",
		Desc:   "Impede apps de usar um identificador para anúncios personalizados.",
		Apply:  []Action{Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\AdvertisingInfo`, "Enabled", 0)},
		Revert: []Action{Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\AdvertisingInfo`, "Enabled", 1)},
	})
	cdm := []string{"SilentInstalledAppsEnabled", "SystemPaneSuggestionsEnabled", "SoftLandingEnabled",
		"RotatingLockScreenOverlayEnabled", "SubscribedContent-338387Enabled", "SubscribedContent-338388Enabled",
		"SubscribedContent-338389Enabled", "SubscribedContent-353694Enabled", "SubscribedContent-353696Enabled"}
	sug := &Tweak{
		ID: "suggestions", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true,
		Title: "Sem dicas, sugestões e apps instalados sozinhos",
		Desc:  "Remove propagandas no Iniciar e na tela de bloqueio e impede a instalação automática de apps sugeridos.",
	}
	for _, n := range cdm {
		sug.Apply = append(sug.Apply, Reg(hkcu, kCDM, n, 0))
		sug.Revert = append(sug.Revert, Reg(hkcu, kCDM, n, 1))
	}
	sug.Apply = append(sug.Apply, Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\UserProfileEngagement`, "ScoobeSystemSettingEnabled", 0))
	sug.Revert = append(sug.Revert, RegDel(hkcu, `Software\Microsoft\Windows\CurrentVersion\UserProfileEngagement`, "ScoobeSystemSettingEnabled"))
	add(sug)
	add(&Tweak{
		ID: "web_search", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true, Reboot: true,
		Title: "Pesquisa do Iniciar sem Bing",
		Desc:  "A busca do menu Iniciar procura só no seu PC — fica mais rápida e não envia o que você digita.",
		Apply: []Action{
			Reg(hkcu, `Software\Policies\Microsoft\Windows\Explorer`, "DisableSearchBoxSuggestions", 1),
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\Search`, "BingSearchEnabled", 0),
		},
		Revert: []Action{
			RegDel(hkcu, `Software\Policies\Microsoft\Windows\Explorer`, "DisableSearchBoxSuggestions"),
			RegDel(hkcu, `Software\Microsoft\Windows\CurrentVersion\Search`, "BingSearchEnabled"),
		},
	})
	add(&Tweak{
		ID: "activity", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true,
		Title: "Desativar histórico de atividades",
		Desc:  "Para de registrar os apps e arquivos que você usa.",
		Apply: []Action{
			Reg(hklm, kPolWin+`\System`, "PublishUserActivities", 0),
			Reg(hklm, kPolWin+`\System`, "UploadUserActivities", 0),
		},
		Revert: []Action{
			RegDel(hklm, kPolWin+`\System`, "PublishUserActivities"),
			RegDel(hklm, kPolWin+`\System`, "UploadUserActivities"),
		},
	})
	add(&Tweak{
		ID: "tailored", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Sem experiências personalizadas",
		Desc:   "Impede a Microsoft de usar seus dados de diagnóstico para dicas e anúncios.",
		Apply:  []Action{Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\Privacy`, "TailoredExperiencesWithDiagnosticDataEnabled", 0)},
		Revert: []Action{Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\Privacy`, "TailoredExperiencesWithDiagnosticDataEnabled", 1)},
	})
	add(&Tweak{
		ID: "feedback", Cat: "priv", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Não pedir feedback",
		Desc:   "Desliga as pesquisas de opinião do Windows.",
		Apply:  []Action{Reg(hkcu, `Software\Microsoft\Siuf\Rules`, "NumberOfSIUFInPeriod", 0)},
		Revert: []Action{RegDel(hkcu, `Software\Microsoft\Siuf\Rules`, "NumberOfSIUFInPeriod")},
	})
	add(&Tweak{
		ID: "copilot", Cat: "priv", Kind: "toggle", Risk: "safe",
		Title: "Desativar Copilot do Windows",
		Desc:  "Remove o botão e a integração do Copilot no sistema.",
		Apply: []Action{
			Reg(hkcu, `Software\Policies\Microsoft\Windows\WindowsCopilot`, "TurnOffWindowsCopilot", 1),
			Reg(hkcu, kAdvanced, "ShowCopilotButton", 0),
		},
		Revert: []Action{
			RegDel(hkcu, `Software\Policies\Microsoft\Windows\WindowsCopilot`, "TurnOffWindowsCopilot"),
			Reg(hkcu, kAdvanced, "ShowCopilotButton", 1),
		},
	})
	add(&Tweak{
		ID: "recall", Cat: "priv", Kind: "toggle", Risk: "safe",
		Title:  "Desativar Recall / capturas de IA",
		Desc:   "Impede o Windows de salvar capturas da tela para análise por IA (em PCs compatíveis).",
		Apply:  []Action{Reg(hkcu, `Software\Policies\Microsoft\Windows\WindowsAI`, "DisableAIDataAnalysis", 1)},
		Revert: []Action{RegDel(hkcu, `Software\Policies\Microsoft\Windows\WindowsAI`, "DisableAIDataAnalysis")},
	})

	// ===================================================================
	// INTERFACE
	// ===================================================================
	add(&Tweak{
		ID: "widgets", Cat: "ui", Kind: "toggle", Risk: "safe", Rec: true, Reboot: true,
		Title:  "Desativar Widgets",
		Desc:   "Remove o painel de notícias e clima, que consome CPU, RAM e internet em segundo plano.",
		Apply:  []Action{Reg(hklm, `SOFTWARE\Policies\Microsoft\Dsh`, "AllowNewsAndInterests", 0)},
		Revert: []Action{RegDel(hklm, `SOFTWARE\Policies\Microsoft\Dsh`, "AllowNewsAndInterests")},
	})
	add(&Tweak{
		ID: "file_ext", Cat: "ui", Kind: "toggle", Risk: "safe", Rec: true,
		Title:  "Mostrar extensões de arquivos",
		Desc:   "Exibe .pdf, .exe, .xlsx etc. Ajuda a identificar arquivos falsos (ex.: fatura.pdf.exe).",
		Apply:  []Action{Reg(hkcu, kAdvanced, "HideFileExt", 0)},
		Revert: []Action{Reg(hkcu, kAdvanced, "HideFileExt", 1)},
	})
	add(&Tweak{
		ID: "explorer_pc", Cat: "ui", Kind: "toggle", Risk: "safe",
		Title:  "Explorer abre em “Este Computador”",
		Desc:   "Em vez da Página Inicial com arquivos recentes (que demora mais para carregar).",
		Apply:  []Action{Reg(hkcu, kAdvanced, "LaunchTo", 1)},
		Revert: []Action{Reg(hkcu, kAdvanced, "LaunchTo", 2)},
	})
	add(&Tweak{
		ID: "recent_files", Cat: "ui", Kind: "toggle", Risk: "safe",
		Title: "Não mostrar arquivos recentes no Explorer",
		Desc:  "Oculta arquivos recentes e pastas frequentes no Acesso Rápido.",
		Apply: []Action{
			Reg(hkcu, kExplorer, "ShowRecent", 0),
			Reg(hkcu, kExplorer, "ShowFrequent", 0),
		},
		Revert: []Action{
			Reg(hkcu, kExplorer, "ShowRecent", 1),
			Reg(hkcu, kExplorer, "ShowFrequent", 1),
		},
	})
	add(&Tweak{
		ID: "start_recs", Cat: "ui", Kind: "toggle", Risk: "safe",
		Title: "Iniciar sem recomendações",
		Desc:  "Remove arquivos recentes e apps sugeridos da seção “Recomendado” do menu Iniciar.",
		Apply: []Action{
			Reg(hkcu, kAdvanced, "Start_TrackDocs", 0),
			Reg(hkcu, kAdvanced, "Start_IrisRecommendations", 0),
		},
		Revert: []Action{
			Reg(hkcu, kAdvanced, "Start_TrackDocs", 1),
			Reg(hkcu, kAdvanced, "Start_IrisRecommendations", 1),
		},
	})
	add(&Tweak{
		ID: "classic_menu", Cat: "ui", Kind: "toggle", Risk: "safe", Reboot: true, CatalogRevert: true,
		Title: "Menu de contexto clássico",
		Desc:  "O botão direito volta a mostrar todas as opções direto, sem “Mostrar mais opções”.",
		Apply: []Action{RegS(hkcu, `Software\Classes\CLSID\{86ca1aa0-34aa-4e8b-a509-50c905bae2a2}\InprocServer32`, "", "")},
		Revert: []Action{Fn("Removendo menu clássico", func(j *Job) error {
			return registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\CLSID\{86ca1aa0-34aa-4e8b-a509-50c905bae2a2}\InprocServer32`)
		}), Fn("", func(j *Job) error {
			_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\CLSID\{86ca1aa0-34aa-4e8b-a509-50c905bae2a2}`)
			return nil
		})},
		Detect: func() int {
			k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\CLSID\{86ca1aa0-34aa-4e8b-a509-50c905bae2a2}\InprocServer32`, registry.QUERY_VALUE)
			if err != nil {
				return 0
			}
			k.Close()
			return 1
		},
	})
	add(&Tweak{
		ID: "taskbar_search", Cat: "ui", Kind: "toggle", Risk: "safe",
		Title:  "Barra de tarefas: pesquisa só como ícone",
		Desc:   "Troca a caixa de pesquisa grande por um ícone, liberando espaço.",
		Apply:  []Action{Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\Search`, "SearchboxTaskbarMode", 1)},
		Revert: []Action{Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\Search`, "SearchboxTaskbarMode", 2)},
	})
	add(&Tweak{
		ID: "storage_sense", Cat: "ui", Kind: "toggle", Risk: "safe", Rec: true,
		Title: "Sensor de Armazenamento automático",
		Desc:  "O Windows apaga arquivos temporários sozinho toda semana. A Lixeira e a pasta Downloads não são tocadas.",
		Apply: []Action{
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\StorageSense\Parameters\StoragePolicy`, "01", 1),
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\StorageSense\Parameters\StoragePolicy`, "2048", 7),
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\StorageSense\Parameters\StoragePolicy`, "04", 1),
		},
		Revert: []Action{
			Reg(hkcu, `Software\Microsoft\Windows\CurrentVersion\StorageSense\Parameters\StoragePolicy`, "01", 0),
		},
		Detect: func() int {
			v := regRead(hkcu, `Software\Microsoft\Windows\CurrentVersion\StorageSense\Parameters\StoragePolicy`, "01")
			return b2i(v.DW == 1)
		},
	})

	// ===================================================================
	// LIMPEZA (ações)
	// ===================================================================
	add(&Tweak{
		ID: "clean_temp", Cat: "clean", Kind: "action", Risk: "safe", Rec: true, Dur: "< 1 min",
		Title: "Arquivos temporários",
		Desc:  "Pastas TEMP do usuário e do Windows. Arquivos em uso são ignorados.",
		Apply: []Action{Fn("", func(j *Job) error {
			total := int64(0)
			for _, d := range []string{os.Getenv("TEMP"), filepath.Join(os.Getenv("SystemRoot"), "Temp")} {
				f, n, s := removeContents(d)
				total += f
				j.out(fmt.Sprintf("• %s: %d itens removidos, %d em uso", d, n, s))
			}
			j.freed(total)
			return nil
		})},
	})
	add(&Tweak{
		ID: "clean_dumps", Cat: "clean", Kind: "action", Risk: "safe", Rec: true, Dur: "< 1 min",
		Title: "Despejos de memória e relatórios de erro",
		Desc:  "Arquivos .dmp de travamentos e relatórios antigos do Windows Error Reporting.",
		Apply: []Action{Fn("", func(j *Job) error {
			pd, sr, la := os.Getenv("ProgramData"), os.Getenv("SystemRoot"), os.Getenv("LOCALAPPDATA")
			total := int64(0)
			for _, d := range []string{
				filepath.Join(sr, "Minidump"), filepath.Join(la, "CrashDumps"),
				filepath.Join(pd, `Microsoft\Windows\WER\ReportArchive`), filepath.Join(pd, `Microsoft\Windows\WER\ReportQueue`),
			} {
				f, _, _ := removeContents(d)
				total += f
			}
			if st, err := os.Stat(filepath.Join(sr, "MEMORY.DMP")); err == nil {
				if os.Remove(filepath.Join(sr, "MEMORY.DMP")) == nil {
					total += st.Size()
				}
			}
			j.freed(total)
			return nil
		})},
	})
	add(&Tweak{
		ID: "clean_wu", Cat: "clean", Kind: "action", Risk: "moderate", Rec: true, Dur: "~1 min",
		Title: "Cache de downloads do Windows Update",
		Desc:  "Atualizações já instaladas que ficaram baixadas. O Windows baixa de novo se precisar.",
		Apply: []Action{
			{Kind: ACmd, Label: "Parando Windows Update", Line: "net stop wuauserv /y & net stop bits /y", Soft: true},
			Fn("", func(j *Job) error {
				f, _, _ := removeContents(filepath.Join(os.Getenv("SystemRoot"), `SoftwareDistribution\Download`))
				j.freed(f)
				return nil
			}),
			{Kind: ACmd, Label: "Reiniciando Windows Update", Line: "net start bits & net start wuauserv", Soft: true},
		},
	})
	add(&Tweak{
		ID: "clean_do", Cat: "clean", Kind: "action", Risk: "safe", Rec: true, Dur: "< 1 min",
		Title: "Cache de Otimização de Entrega",
		Desc:  "Pedaços de atualizações guardados para compartilhar com outros PCs.",
		Apply: []Action{{Kind: APS, Line: "Delete-DeliveryOptimizationCache -Force; 'Cache removido.'", Soft: true}},
	})
	add(&Tweak{
		ID: "clean_thumbs", Cat: "clean", Kind: "action", Risk: "safe", Dur: "< 1 min",
		Title: "Cache de miniaturas",
		Desc:  "Miniaturas de imagens e vídeos do Explorer. São recriadas conforme você navega.",
		Apply: []Action{Fn("", func(j *Job) error {
			dir := filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Windows\Explorer`)
			matches, _ := filepath.Glob(filepath.Join(dir, "thumbcache_*.db"))
			total := int64(0)
			for _, m := range matches {
				if st, err := os.Stat(m); err == nil && os.Remove(m) == nil {
					total += st.Size()
				}
			}
			j.freed(total)
			return nil
		})},
	})
	add(&Tweak{
		ID: "clean_dns", Cat: "clean", Kind: "action", Risk: "safe", Rec: true, Dur: "segundos",
		Title: "Cache de DNS",
		Desc:  "Limpa endereços de sites guardados. Resolve sites que não abrem após mudanças de servidor.",
		Apply: []Action{Cmd("", "ipconfig /flushdns")},
	})
	add(&Tweak{
		ID: "clean_cleanmgr", Cat: "clean", Kind: "action", Risk: "safe", Dur: "5–20 min",
		Title: "Limpeza de Disco completa do Windows",
		Desc:  "Roda a Limpeza de Disco com todas as categorias do sistema (atualizações antigas, logs, instalações anteriores).",
		Warn:  "A Lixeira e a pasta Downloads são excluídas desta limpeza por segurança.",
		Apply: []Action{Fn("", runCleanmgr)},
	})
	add(&Tweak{
		ID: "clean_winsxs", Cat: "clean", Kind: "action", Risk: "safe", Dur: "10–30 min",
		Title: "Limpar componentes antigos (WinSxS)",
		Desc:  "Remove versões substituídas de componentes do Windows (DISM StartComponentCleanup).",
		Apply: []Action{Cmd("DISM", "DISM /Online /Cleanup-Image /StartComponentCleanup")},
	})
	add(&Tweak{
		ID: "clean_recycle", Cat: "clean", Kind: "action", Risk: "moderate", Dur: "segundos",
		Title: "Esvaziar a Lixeira",
		Desc:  "Apaga definitivamente os arquivos da Lixeira de todas as unidades.",
		Warn:  "Não há como recuperar os arquivos depois.",
		Apply: []Action{{Kind: APS, Line: "Clear-RecycleBin -Force; 'Lixeira esvaziada.'", Soft: true}},
	})

	// ===================================================================
	// MANUTENÇÃO (ações)
	// ===================================================================
	add(&Tweak{
		ID: "m_restore", Cat: "maint", Kind: "action", Risk: "safe", Dur: "~1 min",
		Title: "Criar ponto de restauração",
		Desc:  "Salva o estado atual do sistema para poder voltar depois.",
		Apply: []Action{Fn("", createRestorePoint)},
	})
	add(&Tweak{
		ID: "m_dism", Cat: "maint", Kind: "action", Risk: "safe", Rec: true, Dur: "10–20 min",
		Title: "Reparar imagem do Windows (DISM)",
		Desc:  "Verifica e baixa da Microsoft os arquivos corrompidos da instalação.",
		Apply: []Action{Cmd("DISM RestoreHealth", "DISM /Online /Cleanup-Image /RestoreHealth")},
	})
	add(&Tweak{
		ID: "m_sfc", Cat: "maint", Kind: "action", Risk: "safe", Rec: true, Dur: "10–15 min",
		Title: "Verificar arquivos do sistema (SFC)",
		Desc:  "Procura e corrige arquivos do Windows danificados. Rode depois do DISM.",
		Apply: []Action{Cmd("sfc /scannow", "sfc /scannow")},
	})
	add(&Tweak{
		ID: "m_chkdsk", Cat: "maint", Kind: "action", Risk: "safe", Rec: true, Dur: "2–10 min",
		Title: "Verificar erros no disco",
		Desc:  "Analisa o sistema de arquivos do C: sem alterar nada e sem reiniciar (chkdsk /scan).",
		Apply: []Action{Cmd("chkdsk", "chkdsk C: /scan")},
	})
	add(&Tweak{
		ID: "m_trim", Cat: "maint", Kind: "action", Risk: "safe", Rec: true, Dur: "1–5 min",
		Title: "Otimizar unidades (TRIM / desfragmentar)",
		Desc:  "Em SSD faz o TRIM; em HD desfragmenta. O Windows escolhe o certo automaticamente.",
		Apply: []Action{Cmd("defrag", "defrag %SystemDrive% /O /U")},
	})
	add(&Tweak{
		ID: "m_disk_health", Cat: "maint", Kind: "action", Risk: "safe", Rec: true, Dur: "segundos",
		Title: "Saúde dos discos",
		Desc:  "Mostra o tipo (SSD/HD), o estado de saúde e o desgaste de cada disco.",
		Apply: []Action{PS("", `Get-PhysicalDisk | ForEach-Object {
  $r = $_ | Get-StorageReliabilityCounter -ErrorAction SilentlyContinue
  $gb = [math]::Round($_.Size/1GB)
  $w = if ($r -and $r.Wear -ne $null) { "$($r.Wear)%" } else { "n/d" }
  $t = if ($r -and $r.Temperature) { "$($r.Temperature)°C" } else { "n/d" }
  "• $($_.FriendlyName) | $($_.MediaType) | $gb GB | Saúde: $($_.HealthStatus) | Desgaste: $w | Temp.: $t"
}`)},
	})
	add(&Tweak{
		ID: "m_net", Cat: "maint", Kind: "action", Risk: "moderate", Dur: "segundos",
		Title:  "Resetar configurações de rede",
		Desc:   "Restaura Winsock e TCP/IP. Resolve internet lenta ou instável causada por configurações corrompidas.",
		Warn:   "Exige reiniciar. VPNs (ex.: Tailscale) podem precisar reconectar.",
		Reboot: true,
		Apply:  []Action{Cmd("Resetando rede", "netsh winsock reset & netsh int ip reset & ipconfig /flushdns")},
	})
	add(&Tweak{
		ID: "m_icons", Cat: "maint", Kind: "action", Risk: "safe", Dur: "segundos",
		Title: "Reconstruir cache de ícones",
		Desc:  "Corrige ícones em branco ou errados.",
		Apply: []Action{Cmd("", "ie4uinit.exe -show")},
	})

	// Detecção padrão para toggles sem função própria
	for _, t := range tweaks {
		if t.Kind == "toggle" && t.Detect == nil {
			t.Detect = defaultDetect(t)
		}
	}
}

func defaultDetect(t *Tweak) func() int {
	return func() int {
		anySvc, anyExisting := false, false
		for _, a := range t.Apply {
			switch a.Kind {
			case ARegSet:
				if !regMatches(a) {
					return 0
				}
			case ASvc:
				anySvc = true
				cur := svcStart(a.Svc)
				if cur == "" {
					continue
				}
				anyExisting = true
				if cur != a.Start {
					return 0
				}
			}
		}
		if anySvc && !anyExisting {
			return -1
		}
		return 1
	}
}

// ---------------------------------------------------------------------------

func removeContents(dir string) (freed int64, removed, skipped int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			f, r, s := removeContents(p)
			freed += f
			removed += r
			skipped += s
			if os.Remove(p) == nil {
				removed++
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			skipped++
			continue
		}
		if os.Remove(p) == nil {
			freed += info.Size()
			removed++
		} else {
			skipped++
		}
	}
	return
}

func runCleanmgr(j *Job) error {
	const base = `SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\VolumeCaches`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return err
	}
	names, _ := k.ReadSubKeyNames(-1)
	k.Close()
	skip := map[string]bool{"downloadsfolder": true, "recycle bin": true}
	set := []string{}
	for _, n := range names {
		if skip[strings.ToLower(n)] {
			continue
		}
		sk, err := registry.OpenKey(registry.LOCAL_MACHINE, base+`\`+n, registry.SET_VALUE)
		if err != nil {
			continue
		}
		if sk.SetDWordValue("StateFlags0099", 2) == nil {
			set = append(set, n)
		}
		sk.Close()
	}
	j.out(fmt.Sprintf("• %d categorias selecionadas", len(set)))
	before := freeSpace()
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cleanmgr.exe"), "/sagerun:99")
	j.out("• A janela da Limpeza de Disco pode aparecer — aguarde ela terminar sozinha.")
	err = runStreaming(j, cmd)
	for _, n := range set {
		_ = regDelete(hklm, base+`\`+n, "StateFlags0099")
	}
	if after := freeSpace(); after > before {
		j.freed(int64(after - before))
	}
	return err
}

func createRestorePoint(j *Job) error {
	const key = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\SystemRestore`
	orig := regRead(hklm, key, "SystemRestorePointCreationFrequency")
	_ = regWrite(Reg(hklm, key, "SystemRestorePointCreationFrequency", 0))
	err := runPowerShell(j, `Enable-ComputerRestore -Drive "$env:SystemDrive\"; `+
		`Checkpoint-Computer -Description ("WinBoost - " + (Get-Date -Format "dd/MM/yyyy HH:mm")) -RestorePointType MODIFY_SETTINGS; `+
		`"Ponto de restauração criado."`)
	if orig.Exists {
		_ = regWrite(Reg(hklm, key, "SystemRestorePointCreationFrequency", orig.DW))
	} else {
		_ = regDelete(hklm, key, "SystemRestorePointCreationFrequency")
	}
	return err
}
