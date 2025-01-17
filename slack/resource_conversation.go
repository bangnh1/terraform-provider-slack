package slack

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/slack-go/slack"
)

const (
	conversationActionOnDestroyNone    = "none"
	conversationActionOnDestroyArchive = "archive"
)

var (
	conversationActionValidValues = []string{
		conversationActionOnDestroyNone,
		conversationActionOnDestroyArchive,
	}
	validateConversationActionOnDestroyValue = validation.StringInSlice(conversationActionValidValues, false)
)

func resourceSlackConversation() *schema.Resource {
	return &schema.Resource{
		ReadContext:   resourceSlackConversationRead,
		CreateContext: resourceSlackConversationCreate,
		UpdateContext: resourceSlackConversationUpdate,
		DeleteContext: resourceSlackConversationDelete,

		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},

			"topic": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"purpose": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"permanent_members": {
				Type: schema.TypeSet,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Set:      schema.HashString,
				Optional: true,
			},
			"created": {
				Type:     schema.TypeInt,
				Computed: true,
			},
			"creator": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"is_private": {
				Type:     schema.TypeBool,
				Required: true,
			},
			"is_archived": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"is_shared": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"is_ext_shared": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"is_org_shared": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"is_general": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"action_on_destroy": {
				Type:         schema.TypeString,
				Description:  "Either of none or archive",
				Optional:     true,
				Default:      "archive",
				ValidateFunc: validateConversationActionOnDestroyValue,
			},
		},
	}
}

func resourceSlackConversationCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	client := m.(*slack.Client)

	name := d.Get("name").(string)
	isPrivate := d.Get("is_private").(bool)

	channel, err := client.CreateConversationContext(ctx, name, isPrivate)
	if err != nil {
		return diag.Errorf("could not create conversation %s: %s", name, err)
	}

	err = updateChannelMembers(ctx, d, client, channel.ID)
	if err != nil {
		return diag.FromErr(err)
	}

	if topic, ok := d.GetOk("topic"); ok {
		if _, err := client.SetTopicOfConversationContext(ctx, channel.ID, topic.(string)); err != nil {
			return diag.Errorf("couldn't set conversation topic %s: %s", topic.(string), err)
		}
	}

	if purpose, ok := d.GetOk("purpose"); ok {
		if _, err := client.SetPurposeOfConversationContext(ctx, channel.ID, purpose.(string)); err != nil {
			return diag.Errorf("couldn't set conversation purpose %s: %s", purpose.(string), err)
		}
	}

	if isArchived, ok := d.GetOk("is_archived"); ok {
		if isArchived.(bool) {
			err := archiveConversationWithContext(ctx, client, channel.ID)
			if err != nil {
				return diag.FromErr(err)
			}
		}
	}

	d.SetId(channel.ID)
	return resourceSlackConversationRead(ctx, d, m)
}

func updateChannelMembers(ctx context.Context, d *schema.ResourceData, client *slack.Client, channelID string) error {
	members := d.Get("permanent_members").(*schema.Set)

	userIds := schemaSetToSlice(members)
	channel, err := client.GetConversationInfoContext(ctx, channelID, false)
	if err != nil {
		return fmt.Errorf("could not retrieve conversation info for ID %s: %w", channelID, err)
	}

	apiUserInfo, err := client.AuthTest()

	if err != nil {
		return fmt.Errorf("Error authenticating with slack %w", err)
	}
	userIds = remove(userIds, apiUserInfo.UserID)
	userIds = remove(userIds, channel.Creator)

	channelUsers, _, err := client.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{
		ChannelID: channel.ID,
	})

	if err != nil {
		return fmt.Errorf("could not retrieve conversation users for ID %s: %w", channelID, err)
	}

	for _, currentMember := range channelUsers {
		if currentMember != channel.Creator && currentMember != apiUserInfo.UserID && !contains(userIds, currentMember) {
			if err := client.KickUserFromConversationContext(ctx, channelID, currentMember); err != nil {
				return fmt.Errorf("couldn't kick user from conversation: %w", err)
			}
		}
	}

	if len(userIds) > 0 {
		if _, err := client.InviteUsersToConversationContext(ctx, channelID, userIds...); err != nil {
			if err.Error() != "already_in_channel" {
				return fmt.Errorf("couldn't invite users to conversation: %w", err)
			}
		}
	}

	return nil
}

func resourceSlackConversationRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	client := m.(*slack.Client)
	id := d.Id()
	var diags diag.Diagnostics
	channel, err := client.GetConversationInfoContext(ctx, id, false)
	if err != nil {
		if err.Error() == "channel_not_found" {
			diags = append(diags, diag.Diagnostic{
				Severity: diag.Warning,
				Summary:  fmt.Sprintf("channel with ID %s not found, removing from state", id),
			})
			d.SetId("")
			return diags
		}
		return diag.FromErr(fmt.Errorf("couldn't get conversation info for %s: %w", id, err))
	}

	users, _, err := client.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{
		ChannelID: channel.ID,
	})
	if err != nil {
		return diag.FromErr(fmt.Errorf("couldn't get users in conversation for %s: %w", channel.ID, err))
	}
	return updateChannelData(d, channel, users)
}

func resourceSlackConversationUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	client := m.(*slack.Client)

	id := d.Id()

	if d.HasChange("name") {
		if _, err := client.RenameConversationContext(ctx, id, d.Get("name").(string)); err != nil {
			return diag.Errorf("couldn't rename conversation: %s", err)
		}
	}

	if d.HasChange("topic") {
		topic := d.Get("topic")
		if _, err := client.SetTopicOfConversationContext(ctx, id, topic.(string)); err != nil {
			return diag.Errorf("couldn't set conversation topic %s: %s", topic.(string), err)
		}
	}

	if d.HasChange("purpose") {
		purpose := d.Get("purpose")
		if _, err := client.SetPurposeOfConversationContext(ctx, id, purpose.(string)); err != nil {
			return diag.Errorf("couldn't set conversation purpose %s: %s", purpose.(string), err)
		}
	}

	if d.HasChange("is_archived") {
		isArchived := d.Get("is_archived")
		if isArchived.(bool) {
			err := archiveConversationWithContext(ctx, client, id)
			if err != nil {
				return diag.FromErr(err)
			}
		} else {
			if err := client.UnArchiveConversationContext(ctx, id); err != nil {
				if err.Error() != "not_archived" {
					return diag.Errorf("couldn't archive conversation %s: %s", id, err)
				}
			}
		}
	}

	if d.HasChange("permanent_members") {
		err := updateChannelMembers(ctx, d, client, id)
		if err != nil {
			return diag.FromErr(err)
		}
	}

	return resourceSlackConversationRead(ctx, d, m)
}

func resourceSlackConversationDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	client := m.(*slack.Client)

	id := d.Id()
	action := d.Get("action_on_destroy").(string)
	switch action {
	case conversationActionOnDestroyNone:
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  fmt.Sprintf("conversation %s (%s) won't be archived on destroy", id, d.Get("name")),
			Detail:   fmt.Sprintf("action_on_destroy is set to %s which does not archive the conversation ", conversationActionOnDestroyNone),
		})
	case conversationActionOnDestroyArchive:
		err := archiveConversationWithContext(ctx, client, id)
		if err != nil {
			if err.Error() == "channel_not_found" {
				return diags
			}
			return diag.FromErr(err)
		}
	default:
		return diag.Errorf("unknown action_on_destroy value. Valid values are %v", conversationActionValidValues)
	}

	return diags
}

func updateChannelData(d *schema.ResourceData, channel *slack.Channel, users []string) diag.Diagnostics {
	if channel.ID == "" {
		return diag.Errorf("error setting id: returned channel does not have an id")
	}
	d.SetId(channel.ID)

	if err := d.Set("name", channel.Name); err != nil {
		return diag.Errorf("error setting name: %s", err)
	}

	if err := d.Set("topic", channel.Topic.Value); err != nil {
		return diag.Errorf("error setting topic: %s", err)
	}

	if err := d.Set("purpose", channel.Purpose.Value); err != nil {
		return diag.Errorf("error setting purpose: %s", err)
	}

	if err := d.Set("is_archived", channel.IsArchived); err != nil {
		return diag.Errorf("error setting is_archived: %s", err)
	}

	if err := d.Set("is_shared", channel.IsShared); err != nil {
		return diag.Errorf("error setting is_shared: %s", err)
	}

	if err := d.Set("is_ext_shared", channel.IsExtShared); err != nil {
		return diag.Errorf("error setting is_ext_shared: %s", err)
	}

	if err := d.Set("is_org_shared", channel.IsOrgShared); err != nil {
		return diag.Errorf("error setting is_org_shared: %s", err)
	}

	if err := d.Set("created", channel.Created); err != nil {
		return diag.Errorf("error setting created: %s", err)
	}

	if err := d.Set("creator", channel.Creator); err != nil {
		return diag.Errorf("error setting creator: %s", err)
	}

	if err := d.Set("is_private", channel.IsPrivate); err != nil {
		return diag.Errorf("error setting is_private: %s", err)
	}

	if err := d.Set("is_general", channel.IsGeneral); err != nil {
		return diag.Errorf("error setting is_general: %s", err)
	}

	return nil
}

func archiveConversationWithContext(ctx context.Context, client *slack.Client, id string) error {
	if err := client.ArchiveConversationContext(ctx, id); err != nil {
		if err.Error() != "already_archived" {
			return fmt.Errorf("couldn't archive conversation %s: %s", id, err)
		}
	}
	return nil
}

func contains(s []string, e string) bool {
	var found bool
	for _, x := range s {
		if x == e {
			return true
		}
	}
	return found
}
