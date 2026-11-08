package catalog

import "flashcart/internal/domain"

func fixtures() []domain.Product {
	return []domain.Product{
		{SKU: "sku-headphones", Name: "Беспроводные наушники AURA", PriceCents: 1249000, OldPriceCents: 1599000, Version: 1, SellerID: "mesto-studio", SellerName: "Место · Студия", Description: "Мягкие амбушюры, активное шумоподавление и до 40 часов любимой музыки. В комплекте чехол и кабель USB-C.", Category: "electronics", ImageKey: "headphones", InitialStock: 12, Currency: "RUB"},
		{SKU: "sku-keyboard", Name: "Механическая клавиатура TYPE 75", PriceCents: 799000, OldPriceCents: 999000, Version: 1, SellerID: "mesto-studio", SellerName: "Место · Студия", Description: "Компактный формат, тихие линейные переключатели и тёплый оттенок ivory. Подключение USB-C и Bluetooth.", Category: "electronics", ImageKey: "keyboard", InitialStock: 18, Currency: "RUB"},
		{SKU: "sku-monitor", Name: "Монитор LINE 27″, 4K", PriceCents: 2899000, OldPriceCents: 3299000, Version: 1, SellerID: "mesto-studio", SellerName: "Место · Студия", Description: "IPS-экран с тонкой рамкой и регулируемой подставкой. Для работы, творчества и ваших больших идей.", Category: "electronics", ImageKey: "monitor", InitialStock: 7, Currency: "RUB"},
		{SKU: "sku-backpack", Name: "Городской рюкзак WEEKDAY", PriceCents: 449000, OldPriceCents: 599000, Version: 1, SellerID: "seller-forma", SellerName: "FORMA", Description: "Плотный водоотталкивающий текстиль, отделение для ноутбука 15″ и удобные карманы. Цвет: лесной зелёный.", Category: "fashion", ImageKey: "backpack", InitialStock: 6, Currency: "RUB"},
		{SKU: "sku-lamp", Name: "Настольная лампа MOSS", PriceCents: 389000, Version: 1, SellerID: "seller-quiet", SellerName: "Тихий дом", Description: "Мягкий рассеянный свет и скульптурная форма. Три режима яркости для вечернего чтения и уютных разговоров.", Category: "home", ImageKey: "lamp", InitialStock: 4, Currency: "RUB"},
		{SKU: "sku-sneakers", Name: "Кроссовки EVERYDAY, молочные", PriceCents: 699000, OldPriceCents: 849000, Version: 1, SellerID: "seller-forma", SellerName: "FORMA", Description: "Лёгкая пара для города: дышащие материалы, мягкая стелька и нейтральный силуэт. Размер 39.", Category: "fashion", ImageKey: "sneakers", InitialStock: 9, Currency: "RUB"},
		{SKU: "sku-kettle", Name: "Электрический чайник POUR, 1,5 л", PriceCents: 529000, Version: 1, SellerID: "seller-quiet", SellerName: "Тихий дом", Description: "Матовый корпус, точный носик и автоматическое отключение. Маленький ритуал для большого дня.", Category: "home", ImageKey: "kettle", InitialStock: 8, Currency: "RUB"},
		{SKU: "sku-watch", Name: "Смарт-часы PACE, графит", PriceCents: 899000, OldPriceCents: 1099000, Version: 1, SellerID: "seller-motion", SellerName: "MOTION", Description: "Повседневная активность, уведомления и тренировки на запястье. Силиконовый ремешок и зарядка в комплекте.", Category: "sport", ImageKey: "watch", InitialStock: 5, Currency: "RUB"},
		{SKU: "sku-camera", Name: "Камера моментальной печати SNAP", PriceCents: 1199000, Version: 1, SellerID: "mesto-studio", SellerName: "Место · Студия", Description: "Сохраняйте моменты на настоящих карточках. Автоэкспозиция, встроенная вспышка и ремешок. Картриджи приобретаются отдельно.", Category: "electronics", ImageKey: "camera", InitialStock: 3, Currency: "RUB"},
	}
}
