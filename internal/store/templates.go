package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/link"
)

// Templates — каталог шаблонов на диске. Заводится в main и передаётся сюда:
// хранилищу нужно уметь применять шаблон, но не знать, откуда он взялся.
func (s *Store) SetTemplates(dir *devtmpl.Dir) { s.templates = dir }

// TemplateDir возвращает каталог шаблонов.
func (s *Store) TemplateDir() *devtmpl.Dir { return s.templates }

// Template возвращает шаблон по ключу.
func (s *Store) Template(ctx context.Context, key string) (devtmpl.Template, error) {
	if s.templates == nil {
		return devtmpl.Find(key) // на случай вызова до настройки каталога
	}
	return s.templates.Get(key)
}

// ApplyTemplate заводит устройство и разворачивает связки шаблона.
//
// Всё одной транзакцией: половина развёрнутого устройства — это связки,
// которые уже работают, при том что устройства как будто нет.
func (s *Store) ApplyTemplate(ctx context.Context, d Device, t devtmpl.Template,
	assign map[string]devtmpl.Addr) (int64, error) {

	// Проверяем ДО заведения устройства: иначе неверное назначение оставило бы
	// в базе устройство без единой связки.
	if _, err := t.Apply(d.TopicPrefix, assign); err != nil {
		return 0, err
	}

	if strings.TrimSpace(d.Name) == "" {
		d.Name = s.uniqueDeviceName(ctx, t.Name, d.TopicPrefix)
	}
	d.Model, d.Template = t.Model, t.Key

	deviceID, err := s.CreateDevice(ctx, d)
	if err != nil {
		return 0, err
	}

	if err := s.createTemplateLinks(ctx, deviceID, t, assign, nil); err != nil {
		return 0, err
	}
	return deviceID, nil
}

// uniqueDeviceName подбирает название устройству, которому его не дали.
//
// Название профиля само по себе не годится: два одинаковых инвертора получают
// одно и то же имя, и потом ни в списке связок, ни в журнале не понять, о
// каком из них речь. Поэтому у второго и следующих к названию добавляется
// префикс топиков — он у каждого свой по определению.
func (s *Store) uniqueDeviceName(ctx context.Context, name, prefix string) string {
	if name == "" {
		return prefix
	}
	devices, err := s.Devices(ctx)
	if err != nil {
		return name
	}
	for _, d := range devices {
		if d.Name == name {
			return name + " · " + prefix
		}
	}
	return name
}

// createTemplateLinks разворачивает связки шаблона на устройстве.
//
// disabled перечисляет связки по имени, которые надо оставить выключенными:
// при перенастройке устройства человек мог сознательно погасить лишнее, и
// правка назначений не повод это включать.
func (s *Store) createTemplateLinks(ctx context.Context, deviceID int64, t devtmpl.Template,
	assign map[string]devtmpl.Addr, disabled map[string]bool) error {

	links, err := t.Apply(deviceTopicPrefix(ctx, s, deviceID), assign)
	if err != nil {
		return err
	}

	// Метки пар из шаблона превращаются в общий идентификатор: обе стороны
	// канала должны заводиться и удаляться вместе.
	pairIDs := make(map[string]int64)
	for i := range links {
		links[i].DeviceID = deviceID
		if disabled[links[i].Name] {
			links[i].Enabled = false
		}

		pair := pairLabel(t, links[i])
		if pair != "" {
			if id, ok := pairIDs[pair]; ok {
				links[i].PairID = id
			}
		}

		id, err := s.CreateLink(ctx, links[i])
		if err != nil {
			return fmt.Errorf("связка «%s»: %w", links[i].Name, err)
		}
		links[i].ID = id

		if pair != "" {
			if _, ok := pairIDs[pair]; !ok {
				// Пара носит идентификатор первой своей стороны.
				pairIDs[pair] = id
				links[i].PairID = id
				if err := s.UpdateLink(ctx, links[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// deviceTopicPrefix читает префикс устройства: связки строятся от него.
func deviceTopicPrefix(ctx context.Context, s *Store, deviceID int64) string {
	d, err := s.Device(ctx, deviceID)
	if err != nil {
		return ""
	}
	return d.TopicPrefix
}

// pairLabel находит метку пары для развёрнутой связки по её имени: имена
// уникальны внутри шаблона, а порядок при пропуске ролей сдвигается.
func pairLabel(t devtmpl.Template, l link.Link) string {
	for _, spec := range t.Links {
		if spec.Name == l.Name {
			return spec.Pair
		}
	}
	return ""
}

// Assignment восстанавливает, какой элемент назначен каждой роли устройства.
//
// Связки хранят адрес элемента, но не роль: роль — понятие шаблона, а связка
// уже развёрнута. Восстанавливаем по имени: имена внутри шаблона уникальны,
// и по ним связка однозначно опознаётся.
func (s *Store) Assignment(ctx context.Context, deviceID int64, t devtmpl.Template) (map[string]devtmpl.Addr, error) {
	links, err := s.LinksByDevice(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	byName := make(map[string]string, len(t.Links)) // имя связки → роль
	for _, spec := range t.Links {
		byName[spec.Name] = spec.Role
	}

	assign := make(map[string]devtmpl.Addr)
	for _, l := range links {
		role, ok := byName[l.Name]
		if !ok {
			continue // связку добавили руками, к шаблону она отношения не имеет
		}
		assign[role] = devtmpl.Addr{ID: l.TargetID, SubID: l.TargetSubID}
	}
	return assign, nil
}

// ReapplyTemplate перенастраивает уже заведённое устройство.
//
// Связки шаблона заменяются целиком: назначения ролей могли и добавиться, и
// исчезнуть, а вычислять разницу — значит наживать расхождение между тем, что
// в базе, и тем, что показано в форме.
//
// Связки, заведённые к устройству вручную, остаются нетронутыми: их имена в
// шаблоне не встречаются, и трогать чужую работу мы не вправе.
func (s *Store) ReapplyTemplate(ctx context.Context, d Device, t devtmpl.Template,
	assign map[string]devtmpl.Addr) error {

	// Проверяем ДО удаления: иначе неверное назначение оставило бы устройство
	// вовсе без связок.
	if _, err := t.Apply(d.TopicPrefix, assign); err != nil {
		return err
	}

	existing, err := s.LinksByDevice(ctx, d.ID)
	if err != nil {
		return err
	}
	fromTemplate := make(map[string]bool, len(t.Links))
	for _, spec := range t.Links {
		fromTemplate[spec.Name] = true
	}

	// Смена профиля устройства — например, со своей копии обратно на
	// поставляемый. Связки прежнего профиля тоже уходят: их имена в новом
	// могут не встретиться вовсе, и остались бы они висеть у устройства как
	// заведённые руками — с топиками, которых в новом профиле нет.
	if prev, err := s.Device(ctx, d.ID); err == nil && prev.Template != "" && prev.Template != t.Key {
		if old, err := s.Template(ctx, prev.Template); err == nil {
			for _, spec := range old.Links {
				fromTemplate[spec.Name] = true
			}
		}
	}

	// Запоминаем, что было выключено: правка назначений не повод включать
	// обратно то, что человек сознательно погасил.
	disabled := make(map[string]bool)
	for _, l := range existing {
		if !fromTemplate[l.Name] {
			continue
		}
		if !l.Enabled {
			disabled[l.Name] = true
		}
		if err := s.DeleteLink(ctx, l.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}

	d.Model, d.Template = t.Model, t.Key
	if err := s.UpdateDevice(ctx, d); err != nil {
		return err
	}
	return s.createTemplateLinks(ctx, d.ID, t, assign, disabled)
}
