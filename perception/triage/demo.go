package triage

import "strings"

// DemoScenario 构造可直接运行的多租户 SaaS 客服分诊样例。
func DemoScenario(runtime RuntimeContext, message string, budget int) (TriageRequest, []ResourceDocument) {
	if runtime.TenantID == "" {
		runtime.TenantID = "acme"
	}
	if runtime.UserID == "" {
		runtime.UserID = "u-finance-admin"
	}
	if runtime.SessionID == "" {
		runtime.SessionID = "sess-20260613-acme-001"
	}
	if runtime.ProjectID == "" {
		runtime.ProjectID = "workspace-cn-north"
	}
	if runtime.TicketID == "" {
		runtime.TicketID = "T-BILLING-1024"
	}
	if runtime.ProductLine == "" {
		runtime.ProductLine = "customer-success-suite"
	}
	if runtime.Plan == "" {
		runtime.Plan = "enterprise"
	}
	if strings.TrimSpace(message) == "" {
		message = "客户说本月发票金额和后台用量不一致，还担心我们看到其他子公司的数据。请先判断怎么处理。"
	}

	manualBilling := TenantHandle("manual_section", runtime.TenantID, "billing/invoice-reconciliation")
	manualPermission := TenantHandle("manual_section", runtime.TenantID, "security/tenant-data-boundary")
	ticketSimilar := TenantHandle("ticket", runtime.TenantID, "T-9101")
	runbookInvoice := TenantHandle("runbook", runtime.TenantID, "billing-diff-check")

	profile := TenantProfile{
		TenantID:   runtime.TenantID,
		TenantName: "Acme SaaS Ltd.",
		ProductConfig: map[string]string{
			"enabled_modules":     "billing,audit-log,sso,usage-metering",
			"billing_currency":    "CNY",
			"data_region":         "cn-north-1",
			"support_sla_minutes": "30",
		},
		CurrentTicket: TicketContext{
			TicketID: runtime.TicketID,
			Status:   "open",
			Severity: "P1",
			Summary:  "发票金额与用量报表不一致，客户要求解释并确认租户数据隔离。",
			Signals: []string{
				"invoice_id=INV-202606-ACME-08",
				"usage_report_range=2026-05-01..2026-05-31",
				"billing job warning: timeout while loading discount snapshot, retry succeeded",
			},
		},
		RecentTurns: []ConversationTurn{
			{Role: "user", Content: "我们财务看到发票比用量页多了 18%，是不是算错了？"},
			{Role: "assistant", Content: "我会先确认当前租户、套餐、账单周期和折扣快照，不会查询其他租户资料。"},
			{Role: "user", Content: "另外我们集团下面有多个子公司，你们客服会不会看到别的子公司数据？"},
		},
		ManualIndexes: []KnowledgeIndex{
			{Title: "账单与发票核对", Summary: "解释发票金额、用量报表、折扣快照和税费字段的核对顺序。", Handle: manualBilling},
			{Title: "租户数据边界", Summary: "说明 tenant_id、workspace_id、子公司权限边界和客服可见范围。", Handle: manualPermission},
		},
		FAQSummaries: []KnowledgeIndex{
			{Title: "为什么发票金额和用量页不同", Summary: "常见原因包括折扣延迟、税费、跨周期补扣、失败重试后的最终账单快照。", Handle: TenantHandle("faq", runtime.TenantID, "invoice-vs-usage")},
		},
		SimilarTicketSummaries: []KnowledgeIndex{
			{Title: "T-9101 发票差异", Summary: "同租户上月曾因折扣快照延迟导致发票差异，最终以账单快照为准并补充审计记录。", Handle: ticketSimilar},
		},
		DeferredResources: []ResourceRef{
			{Kind: "manual_section", ID: "billing/invoice-reconciliation", Title: "账单与发票核对手册", Handle: manualBilling, TenantID: runtime.TenantID},
			{Kind: "manual_section", ID: "security/tenant-data-boundary", Title: "租户数据边界手册", Handle: manualPermission, TenantID: runtime.TenantID},
			{Kind: "ticket", ID: "T-9101", Title: "相似历史工单原文 T-9101", Handle: ticketSimilar, TenantID: runtime.TenantID},
			{Kind: "runbook", ID: "billing-diff-check", Title: "账单差异排查 runbook", Handle: runbookInvoice, TenantID: runtime.TenantID},
		},
	}

	docs := []ResourceDocument{
		{
			TenantID: runtime.TenantID,
			Handle:   manualBilling,
			Kind:     "manual_section",
			Title:    "账单与发票核对手册",
			Content: strings.Join([]string{
				"核对顺序：先锁定 tenant_id、账单周期、invoice_id，再比较 usage-metering 汇总、折扣快照、税费和补扣项。",
				"如果折扣快照加载超时但重试成功，应以最终账单快照为准，同时给客户解释审计日志中的 retry succeeded。",
				"客服不得查看其他 tenant_id 的用量明细，只能读取当前 tenant 的账单和审计记录。",
			}, "\n"),
		},
		{
			TenantID: runtime.TenantID,
			Handle:   manualPermission,
			Kind:     "manual_section",
			Title:    "租户数据边界手册",
			Content: strings.Join([]string{
				"所有客服查询必须携带 tenant_id 和 workspace_id。",
				"集团客户的子公司数据默认按 workspace 隔离，除非当前用户拥有 group_billing_admin 权限。",
				"跨 tenant handle 一律拒绝读取，并记录安全事件。",
			}, "\n"),
		},
		{
			TenantID: runtime.TenantID,
			Handle:   ticketSimilar,
			Kind:     "ticket",
			Title:    "相似历史工单原文 T-9101",
			Content:  "T-9101: 2026-05 发票差异。根因是折扣快照延迟 11 分钟，最终账单无误；客服回复包含审计记录链接和下一账期监控承诺。",
		},
		{
			TenantID: runtime.TenantID,
			Handle:   runbookInvoice,
			Kind:     "runbook",
			Title:    "账单差异排查 runbook",
			Content:  "Runbook: 1) 验证 tenant_id 2) 拉取 invoice snapshot 3) 拉取 usage summary 4) 对比 discount snapshot 5) 若差异超过 5% 升级 Billing SRE。",
		},
	}

	return TriageRequest{
		Runtime:      runtime,
		Tenant:       profile,
		Message:      message,
		Budget:       budget,
		IncludeTrace: true,
	}, docs
}
