package setup

import "strings"

const (
	componentPrefix = "p2setup"

	componentKindTicket     = "ticket"
	componentKindOnboarding = "onboard"
	componentKindWizard     = "wizard"

	ticketOpOpen       = "open"
	ticketOpAction     = "action"
	ticketOpCloseModal = "close_modal"

	onboardingOpAck        = "ack"
	onboardingOpRoleSelect = "roles"

	wizardOpTemplate = "template"
	wizardOpVerify   = "verify"
	wizardOpScratch  = "scratch"
	wizardOpAction   = "action"
	wizardOpModal    = "modal"
)

func IsPersistentComponentID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), componentPrefix+":")
}

func TicketOpenCustomID(panelID, departmentID string) string {
	return strings.Join([]string{componentPrefix, componentKindTicket, ticketOpOpen, cleanPart(panelID), cleanPart(departmentID)}, ":")
}

func ParseTicketOpenCustomID(id string) (panelID string, departmentID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 5 || parts[0] != componentPrefix || parts[1] != componentKindTicket || parts[2] != ticketOpOpen {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func TicketActionCustomID(action, ticketID string) string {
	return strings.Join([]string{componentPrefix, componentKindTicket, ticketOpAction, cleanPart(action), cleanPart(ticketID)}, ":")
}

func ParseTicketActionCustomID(id string) (action string, ticketID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 5 || parts[0] != componentPrefix || parts[1] != componentKindTicket || parts[2] != ticketOpAction {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func TicketCloseModalID(ticketID string) string {
	return strings.Join([]string{componentPrefix, componentKindTicket, ticketOpCloseModal, cleanPart(ticketID)}, ":")
}

func ParseTicketCloseModalID(id string) (ticketID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 4 || parts[0] != componentPrefix || parts[1] != componentKindTicket || parts[2] != ticketOpCloseModal {
		return "", false
	}
	return parts[3], true
}

func OnboardingAcknowledgeCustomID(flowID string) string {
	return strings.Join([]string{componentPrefix, componentKindOnboarding, onboardingOpAck, cleanPart(flowID)}, ":")
}

func ParseOnboardingAcknowledgeCustomID(id string) (flowID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 4 || parts[0] != componentPrefix || parts[1] != componentKindOnboarding || parts[2] != onboardingOpAck {
		return "", false
	}
	return parts[3], true
}

func OnboardingRoleSelectCustomID(flowID, stepID string) string {
	return strings.Join([]string{componentPrefix, componentKindOnboarding, onboardingOpRoleSelect, cleanPart(flowID), cleanPart(stepID)}, ":")
}

func ParseOnboardingRoleSelectCustomID(id string) (flowID string, stepID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 5 || parts[0] != componentPrefix || parts[1] != componentKindOnboarding || parts[2] != onboardingOpRoleSelect {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func WizardTemplateSelectCustomID(projectID string) string {
	return strings.Join([]string{componentPrefix, componentKindWizard, wizardOpTemplate, cleanPart(projectID)}, ":")
}

func ParseWizardTemplateSelectCustomID(id string) (projectID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 4 || parts[0] != componentPrefix || parts[1] != componentKindWizard || parts[2] != wizardOpTemplate {
		return "", false
	}
	return parts[3], true
}

func WizardVerificationSelectCustomID(projectID string) string {
	return strings.Join([]string{componentPrefix, componentKindWizard, wizardOpVerify, cleanPart(projectID)}, ":")
}

func ParseWizardVerificationSelectCustomID(id string) (projectID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 4 || parts[0] != componentPrefix || parts[1] != componentKindWizard || parts[2] != wizardOpVerify {
		return "", false
	}
	return parts[3], true
}

func WizardScratchSelectCustomID(projectID, field string) string {
	return strings.Join([]string{componentPrefix, componentKindWizard, wizardOpScratch, cleanPart(projectID), cleanPart(field)}, ":")
}

func ParseWizardScratchSelectCustomID(id string) (projectID string, field string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 5 || parts[0] != componentPrefix || parts[1] != componentKindWizard || parts[2] != wizardOpScratch {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func WizardActionCustomID(projectID, action string) string {
	return strings.Join([]string{componentPrefix, componentKindWizard, wizardOpAction, cleanPart(projectID), cleanPart(action)}, ":")
}

func ParseWizardActionCustomID(id string) (projectID string, action string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 5 || parts[0] != componentPrefix || parts[1] != componentKindWizard || parts[2] != wizardOpAction {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func WizardModalCustomID(projectID, group string) string {
	return strings.Join([]string{componentPrefix, componentKindWizard, wizardOpModal, cleanPart(projectID), cleanPart(group)}, ":")
}

func ParseWizardModalCustomID(id string) (projectID string, group string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 5 || parts[0] != componentPrefix || parts[1] != componentKindWizard || parts[2] != wizardOpModal {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func cleanPart(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, ":", "_")
	return value
}
