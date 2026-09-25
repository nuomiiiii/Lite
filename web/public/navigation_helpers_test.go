package public

func (navigation ThemeNavigation) ServerNetworkURL(uuid string) string {
	if validThemeServerRouteTemplate(navigation.serverNetworkTemplate, true) {
		return navigation.serverURL(navigation.serverNetworkTemplate, uuid, 0)
	}
	return navigation.ServerDetailURL(uuid, 0)
}
